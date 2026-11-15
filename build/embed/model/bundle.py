"""Builds the model bundle stratus-embed runs (#505), from Google's SigLIP2.

    see the Dockerfile beside this for how it is run

Every number in here was measured in the spike on #505, and the reason for
each step is beside it. What comes out is /out/<NAME>.tar.gz holding
vision.ort, text.ort, tokenizer.bin, NAME and the licence, and the reference
vectors internal/embed's test compares the helper with.
"""

import gzip
import hashlib
import json
import os
import struct
import subprocess
import sys
import tarfile
import urllib.request
from pathlib import Path

import numpy as np
import onnx
import onnxruntime as ort
import torch
from onnx import TensorProto, helper, numpy_helper
from onnxruntime.quantization import (CalibrationDataReader, CalibrationMethod, QuantFormat, QuantType,
                                      quantize_dynamic, quantize_static)
from onnxruntime.quantization.shape_inference import quant_pre_process
from PIL import Image
from transformers import AutoModel, AutoTokenizer

REPO = "google/siglip2-base-patch16-224"
REVISION = "75de2d55ec2d0b4efc50b3e9ad70dba96a7b2fa2"
NAME = "siglip2-base-patch16-224-int8.2"
HERE = Path(__file__).parent
WORK = Path("/work")
OUT = Path("/out")

# The one matrix of the text tower that int8 ruins for anything but English:
# Spanish fell from 0.79 to 0.73 with it quantised and English did not notice.
KEEP_FLOAT = ["/text_model/encoder/layers.0/mlp/fc2/MatMul"]


def pixels(path):
    """PIL's bilinear to 224, scaled to [-1, 1]: what internal/embed.Pixels does."""
    img = Image.open(path).convert("RGB").resize((224, 224), Image.BILINEAR)
    return (np.asarray(img, dtype=np.float32) / 127.5 - 1).transpose(2, 0, 1)[None].copy()


def export(model):
    model.requires_grad_(False)

    class Vision(torch.nn.Module):
        def __init__(self):
            super().__init__()
            self.m = model

        def forward(self, pixel_values):
            return torch.nn.functional.normalize(self.m.get_image_features(pixel_values=pixel_values), dim=-1)

    class Text(torch.nn.Module):
        def __init__(self):
            super().__init__()
            self.m = model

        def forward(self, input_ids):
            return torch.nn.functional.normalize(self.m.get_text_features(input_ids=input_ids), dim=-1)

    torch.onnx.export(Vision(), (torch.zeros(1, 3, 224, 224),), WORK / "vision.onnx",
                      input_names=["pixel_values"], output_names=["embedding"],
                      dynamic_axes={"pixel_values": {0: "n"}}, opset_version=17)
    torch.onnx.export(Text(), (torch.zeros(1, 64, dtype=torch.long),), WORK / "text.onnx",
                      input_names=["input_ids"], output_names=["embedding"],
                      dynamic_axes={"input_ids": {0: "n"}}, opset_version=17)


def calibration_images():
    """The 64 COCO photographs the vision tower's ranges are measured on, none
    of them among the 500 the spike evaluated with, pinned by digest."""
    paths = []
    for line in (HERE / "calibration.sha256").read_text().split("\n"):
        if not line.strip():
            continue
        digest, name = line.split()
        path = WORK / "calib" / name
        if not path.exists():
            path.parent.mkdir(parents=True, exist_ok=True)
            urllib.request.urlretrieve(f"http://images.cocodataset.org/val2017/{name}", path)
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            sys.exit(f"calibration image {name} is not the one pinned")
        paths.append(path)
    return paths


def quantize_vision():
    """Static int8, calibrated: dynamic int8 took the tower from 0.81 to 0.54,
    this takes it to 0.79 at a third of the time. MatMul only, since ONNX
    Runtime has no integer Conv for the one patch embedding."""

    class Reader(CalibrationDataReader):
        def __init__(self):
            self.it = iter([{"pixel_values": pixels(p)} for p in calibration_images()])

        def get_next(self):
            return next(self.it, None)

    quant_pre_process(WORK / "vision.onnx", WORK / "vision.pre.onnx", skip_symbolic_shape=True)
    quantize_static(WORK / "vision.pre.onnx", WORK / "vision.q.onnx", Reader(), quant_format=QuantFormat.QDQ,
                    per_channel=True, weight_type=QuantType.QInt8, activation_type=QuantType.QUInt8,
                    op_types_to_quantize=["MatMul"], calibrate_method=CalibrationMethod.Percentile,
                    extra_options={"CalibPercentile": 99.99})


def vocabulary_table(graph):
    for node in graph.node:
        if node.op_type != "Gather":
            continue
        init = next((i for i in graph.initializer if i.name == node.input[0]), None)
        if init is not None and len(init.dims) == 2 and init.dims[0] >= 100000:
            return node, init
    sys.exit("no vocabulary table in the text tower")


def quantize_text():
    """Dynamic int8 on every MatMul but KEEP_FLOAT, and the 256k-token table
    int8 per row: 291 MB, of which a query touches the rows it uses.

    The table is dequantised in float32 after the lookup. It was fp16 at first,
    which an arm64 build runs and an amd64 one has no kernel for -- the first
    publish of this bundle failed to load on every x86 machine."""
    quantize_dynamic(WORK / "text.onnx", WORK / "text.q8.onnx", weight_type=QuantType.QInt8,
                     op_types_to_quantize=["MatMul"], nodes_to_exclude=KEEP_FLOAT)

    m = onnx.load(WORK / "text.q8.onnx")
    node, init = vocabulary_table(m.graph)
    w = numpy_helper.to_array(init).astype(np.float32)
    scale = (np.abs(w).max(1) / 127).astype(np.float32)
    scale[scale == 0] = 1
    q = np.round(w / scale[:, None]).astype(np.int8)
    m.graph.initializer.remove(init)
    m.graph.initializer.extend([numpy_helper.from_array(q, "tok_q"),
                                numpy_helper.from_array(scale[:, None], "tok_s")])
    ids, out = node.input[1], node.output[0]
    at = list(m.graph.node).index(node)
    m.graph.node.remove(node)
    for i, n in enumerate([
        helper.make_node("Gather", ["tok_q", ids], ["tok_qg"], axis=0),
        helper.make_node("Gather", ["tok_s", ids], ["tok_sg"], axis=0),
        helper.make_node("Cast", ["tok_qg"], ["tok_qf"], to=TensorProto.FLOAT),
        helper.make_node("Mul", ["tok_qf", "tok_sg"], [out]),
    ]):
        m.graph.node.insert(at + i, n)
    onnx.save(m, WORK / "text.q.onnx")


def to_ort(bundle):
    """ORT format, which is what lets the helper map a model instead of parsing
    it. Converted for arm because that is the conversion with nothing
    platform-specific in it: amd64's adds NCHWc layouts an arm build cannot run,
    and the activations are uint8, which the arm flag does not touch."""
    conv = WORK / "ort"
    conv.mkdir(exist_ok=True)
    for tower in ("vision", "text"):
        (conv / f"{tower}.onnx").write_bytes((WORK / f"{tower}.q.onnx").read_bytes())
    subprocess.run([sys.executable, "-m", "onnxruntime.tools.convert_onnx_models_to_ort",
                    "--optimization_style", "Fixed", "--target_platform", "arm", str(conv)], check=True)
    for tower in ("vision", "text"):
        (bundle / f"{tower}.ort").write_bytes((conv / f"{tower}.ort").read_bytes())
    ops = [l for l in (conv / "required_operators.config").read_text().splitlines() if not l.startswith("#")]
    if ops != [l for l in (HERE.parent / "ops.config").read_text().splitlines() if l]:
        sys.exit("the towers need other operators than build/embed/ops.config builds in:\n" + "\n".join(ops))


def tokenizer_bin(tok_json, out):
    """tokenizer.json in the form internal/embed.ReadTokenizer reads: the JSON
    is 33 MB and parsing it costs more memory than the tower it feeds."""
    t = json.loads(Path(tok_json).read_text())
    vocab = t["model"]["vocab"]
    tokens = [None] * len(vocab)
    for s, i in vocab.items():
        tokens[i] = s
    with open(out, "wb") as f:
        f.write(b"STK1" + struct.pack("<I", len(tokens)))
        for s in tokens:
            b = s.encode()
            f.write(struct.pack("<H", len(b)) + b)
        merges = [m if isinstance(m, list) else m.split(" ", 1) for m in t["model"]["merges"]]
        f.write(struct.pack("<I", len(merges)))
        for a, b in merges:
            f.write(struct.pack("<II", vocab[a], vocab[b]))
        added = sorted(a["id"] for a in t["added_tokens"])
        f.write(struct.pack("<I", len(added)) + struct.pack(f"<{len(added)}I", *added))


def reference(bundle, tokenizer):
    """The vectors internal/embed's test holds the helper to, for its picture
    and its query, made by this ONNX Runtime from this bundle."""
    testdata = HERE.parent.parent.parent / "internal" / "embed" / "testdata"
    tokens = json.loads((testdata / "siglip2_tokens.json").read_text())[0]
    ids = tokenizer(tokens["text"].lower(), padding="max_length", max_length=64, truncation=True)["input_ids"]
    if ids != tokens["ids"]:
        sys.exit("the tokenizer no longer gives the ids the Go test was written against")
    v = ort.InferenceSession(str(bundle / "vision.ort")).run(None, {"pixel_values": pixels(testdata / "pixels_src.png")})[0][0]
    t = ort.InferenceSession(str(bundle / "text.ort")).run(None, {"input_ids": np.array([ids], np.int64)})[0][0]
    return {"image": [round(float(x), 6) for x in v], "query": tokens["text"], "text": [round(float(x), 6) for x in t]}


def main():
    WORK.mkdir(exist_ok=True)
    bundle = WORK / NAME
    bundle.mkdir(exist_ok=True)

    model = AutoModel.from_pretrained(REPO, revision=REVISION).eval()
    tokenizer = AutoTokenizer.from_pretrained(REPO, revision=REVISION)
    export(model)
    quantize_vision()
    quantize_text()
    to_ort(bundle)

    from huggingface_hub import hf_hub_download
    tokenizer_bin(hf_hub_download(REPO, "tokenizer.json", revision=REVISION), bundle / "tokenizer.bin")
    (bundle / "NAME").write_text(NAME + "\n")
    for f in ("LICENSE", "NOTICE"):
        (bundle / f).write_text((HERE / f).read_text())

    OUT.mkdir(exist_ok=True)
    (OUT / "siglip2_vectors.json").write_text(json.dumps(reference(bundle, tokenizer)))

    # Same bytes from the same files: sorted, no owner, no time.
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
    os.environ.setdefault("HF_HUB_DISABLE_TELEMETRY", "1")
    main()
