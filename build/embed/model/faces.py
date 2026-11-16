"""Builds the face bundle stratus-embed runs (#519), from OpenCV Zoo's YuNet and SFace.

    see the Dockerfile beside this for how it is run

Nothing is trained or quantised here: the two graphs are OpenCV's, pinned by
digest, cleaned of what stops ONNX Runtime folding them and converted to the
ORT format. What comes out is /out/<NAME>.tar.gz holding yunet.ort, sface.ort,
NAME and the licences, and the reference outputs internal/embed's test
compares the helper with.
"""

import gzip
import hashlib
import json
import os
import subprocess
import sys
import tarfile
import urllib.request
from pathlib import Path

import numpy as np
import onnx
import onnxruntime as ort
from PIL import Image

ZOO = "https://github.com/opencv/opencv_zoo/raw/48b10fa505aeb51fbd127fc60a5c660040e823b7/models"
# fp32 both: YuNet is 230 kB either way, and OpenCV's int8 SFace put a face's
# vector at a median 0.975 of the float one's, and as low as 0.82, on the
# hundred faces in the spike's COCO photographs -- for a model whose whole
# job is a cosine against a threshold, 28 MB is cheap.
SOURCES = {
    "yunet": (f"{ZOO}/face_detection_yunet/face_detection_yunet_2023mar.onnx",
              "8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4"),
    "sface": (f"{ZOO}/face_recognition_sface/face_recognition_sface_2021dec.onnx",
              "0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79"),
}
NAME = "faces-yunet-2023mar-sface-2021dec.1"
HERE = Path(__file__).parent
WORK = Path("/work/faces")
OUT = Path("/out")

# What the reference keeps of each output: every STRIDEth value, which is
# enough to catch a wrong layout or a wrong kernel without a megabyte of JSON.
STRIDE = 64


def fetch(name):
    url, digest = SOURCES[name]
    path = WORK / f"{name}.src.onnx"
    if not path.exists():
        urllib.request.urlretrieve(url, path)
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        sys.exit(f"{name} is not the model pinned")
    return path


def clean(src, dst):
    """SFace lists its weights among its inputs, which makes them inputs a
    caller could override and so not constants ONNX Runtime may fold: its
    batch normalisations would stay unfused, and the helper reads input 0."""
    m = onnx.load(src)
    weights = {i.name for i in m.graph.initializer}
    inputs = [i for i in m.graph.input if i.name not in weights]
    del m.graph.input[:]
    m.graph.input.extend(inputs)
    onnx.save(m, dst)


def to_ort(bundle):
    """As bundle.py does, and for its reason: converted for arm, the one
    conversion with nothing platform-specific in it."""
    conv = WORK / "ort"
    conv.mkdir(exist_ok=True)
    for name in SOURCES:
        clean(fetch(name), conv / f"{name}.onnx")
    subprocess.run([sys.executable, "-m", "onnxruntime.tools.convert_onnx_models_to_ort",
                    "--optimization_style", "Fixed", "--target_platform", "arm", str(conv)], check=True)
    for name in SOURCES:
        (bundle / f"{name}.ort").write_bytes((conv / f"{name}.ort").read_bytes())
    built = operators((HERE.parent / "ops.config").read_text())
    for domain, ops in operators((conv / "required_operators.config").read_text()).items():
        if missing := ops - built.get(domain, set()):
            sys.exit(f"the face models need operators build/embed/ops.config does not build in: {domain} {sorted(missing)}")


def operators(config):
    """An operator config as {(domain, opset): {op}}."""
    out = {}
    for line in config.splitlines():
        if line and not line.startswith("#"):
            domain, opset, ops = line.split(";")
            out[(domain, int(opset))] = set(ops.split(","))
    return out


def inputs():
    """The tensors the reference is made of, built as the Go test builds them:
    the test picture in the top left corner of YuNet's 640 square, BGR and
    unscaled, and its top left 112 square for SFace, RGB and unscaled."""
    testdata = HERE.parent.parent.parent / "internal" / "embed" / "testdata"
    px = np.asarray(Image.open(testdata / "pixels_src.png").convert("RGB"), np.float32)
    det = np.zeros((640, 640, 3), np.float32)
    det[:px.shape[0], :px.shape[1]] = px[:, :, ::-1]
    return det.transpose(2, 0, 1)[None].copy(), px[:112, :112].transpose(2, 0, 1)[None].copy()


def reference(bundle):
    det, rec = inputs()
    yunet = ort.InferenceSession(str(bundle / "yunet.ort"))
    sface = ort.InferenceSession(str(bundle / "sface.ort"))
    # Significant figures and not decimals: a sigmoid with no face under it is
    # 1e-5, which five decimals turn into a zero.
    figures = lambda a: [float(f"{x:.6g}") for x in a]
    sample = lambda a: figures(a.reshape(-1)[::STRIDE])
    return {
        "stride": STRIDE,
        "yunet": [{"name": o.name, "size": int(v.size), "sample": sample(v)}
                  for o, v in zip(yunet.get_outputs(), yunet.run(None, {yunet.get_inputs()[0].name: det}))],
        "sface": figures(sface.run(None, {sface.get_inputs()[0].name: rec})[0][0]),
    }


def main():
    WORK.mkdir(parents=True, exist_ok=True)
    bundle = WORK / NAME
    bundle.mkdir(exist_ok=True)
    to_ort(bundle)
    (bundle / "NAME").write_text(NAME + "\n")
    for f in ("LICENSE", "NOTICE"):
        (bundle / f).write_text((HERE / f"faces.{f}").read_text())

    OUT.mkdir(exist_ok=True)
    (OUT / "faces_outputs.json").write_text(json.dumps(reference(bundle)))

    archive = OUT / f"{NAME}.tar.gz"
    with open(archive, "wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as gz, \
            tarfile.open(fileobj=gz, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for f in sorted(bundle.iterdir()):
            info = tar.gettarinfo(str(f), arcname=f"{NAME}/{f.name}")
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            info.mtime = 0
            with open(f, "rb") as body:
                tar.addfile(info, body)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (OUT / f"{NAME}.tar.gz.sha256").write_text(f"{digest}  {archive.name}\n")
    print(archive, archive.stat().st_size, digest)


if __name__ == "__main__":
    main()
