# syntax=docker/dockerfile:1

# The recipe for the model bundles stratus-embed runs (#505): bundle.py, in the
# Python it was measured with. Run by hand when the model changes, which is
# roughly never, and the result published as a release asset:
#
#   docker build -t stratus-embed-model -f build/embed/model/Dockerfile build/embed
#   docker run --rm -v "$PWD/out":/out \
#     -v "$PWD/internal/embed/testdata":/src/internal/embed/testdata:ro stratus-embed-model
#
# and faces.py the same way for the face bundle (#519), by naming it after the
# image: `... stratus-embed-model faces.py`.
#
# The context is build/embed so that the operators the helper is built with
# are what the conversion is checked against.
#
# Two runs give the same model and not the same bytes: the vectors are equal to
# the last bit, but the ORT format serialises some tables in an order of its
# own, so what internal/embed.SigLIP2 pins is the asset as published.
#
# The versions are pinned because they decide the model: a different torch
# exports different node names, and KEEP_FLOAT names one.
FROM python:3.12-slim

RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.5.1 \
 && pip install --no-cache-dir transformers==4.51.3 huggingface_hub==0.36.2 onnx==1.17.0 \
      onnxruntime==1.24.4 flatbuffers==25.2.10 sentencepiece==0.2.0 protobuf==5.29.4 \
      pillow==11.1.0 numpy==2.2.4 ml_dtypes==0.5.1

WORKDIR /src/build/embed/model
COPY ops.config /src/build/embed/ops.config
COPY model/ .
ENTRYPOINT ["python"]
CMD ["bundle.py"]
