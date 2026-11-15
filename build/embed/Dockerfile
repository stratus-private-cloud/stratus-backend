# syntax=docker/dockerfile:1

# A statically linked stratus-embed: main.cc around an ONNX Runtime built for
# exactly the operators the two SigLIP2 towers use (ops.config), published so the
# server image can COPY it in one line, as build/ffmpeg is.
#
# A minimal build: it reads the .ort format and nothing else, which is what lets
# it map a model instead of parsing one into memory.
ARG ALPINE_VERSION=3.24
ARG ORT_VERSION=1.24.4
# What the image is tagged with: the ONNX Runtime it is built on and a count
# of changes to main.cc or the operators since, so either is a new tag.
ARG EMBED_VERSION=1.24.4-1

FROM alpine:${ALPINE_VERSION} AS build
ARG ORT_VERSION
SHELL ["/bin/ash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3018
RUN apk add --no-cache build-base cmake samurai python3 py3-flatbuffers curl patch linux-headers git

# Alpine builds this same release on musl, and its patches are what make that
# work. They are in patches/ rather than fetched: Alpine's GitLab answers a
# GitHub runner with 418. The digests are the ones its own APKBUILD carries.
WORKDIR /src
COPY patches/ /src/
RUN curl -fsSL -o ort.tar.gz "https://github.com/microsoft/onnxruntime/archive/refs/tags/v${ORT_VERSION}.tar.gz" \
 && sha512sum -c <<'SUMS'
53b3aed02e085c83aa3c7e1faffeae7f70675d6770bc44d148085ded47940f29471eef76e0c4565bbb471f0f814610e0d1892ce9305c63d011fe851a22b1f7ab  ort.tar.gz
976913be90b0a82ff1ba403f46306ef4e5939bb05296227c99d3c4a609dd00f5750f9b1c6c30d20791ff0724c1bcffc6aa498eac906257aefefc6f35df796fe0  no-execinfo.patch
8f6129686fc3f241df174b8a86e957642e0b0a3a9e2ef15bea98ddea199291b444b6b39e4f56f8a7a2e64ee03113ad140fd664a7b6f33a8a9e23c40e883e08df  0001-Remove-MATH_NO_EXCEPT-macro.patch
7484dce0ba435f72a4186d43586cad1ab5867328a6cd190359b71af72d0c788d52fcf730ed59efd05e5383fe9b3f91bb7f17e5507738ba6d297e3868008eaeb6  gcc-15.patch
SUMS
RUN tar xzf ort.tar.gz && mv "onnxruntime-${ORT_VERSION}" ort \
 && cd ort && for p in no-execinfo 0001-Remove-MATH_NO_EXCEPT-macro gcc-15; do patch -p1 < "../$p.patch"; done

COPY ops.config /src/ops.config
WORKDIR /src/ort
RUN python3 tools/ci_build/build.py --build_dir /build --config Release --update --build --parallel \
      --minimal_build --include_ops_by_config /src/ops.config --disable_ml_ops --disable_rtti \
      --skip_tests --skip_submodule_sync --cmake_generator Ninja --allow_running_as_root \
      --cmake_extra_defines CMAKE_POLICY_VERSION_MINIMUM=3.5 onnxruntime_BUILD_UNIT_TESTS=OFF

COPY main.cc /src/main.cc
# Every static archive the build left, in one group: ONNX Runtime has no single
# static library and the order between its parts is a cycle.
RUN g++ -static -O2 -s -std=c++17 /src/main.cc -I include/onnxruntime/core/session -o /stratus-embed \
      -Wl,--start-group $(find /build/Release -name '*.a' | sort) -Wl,--end-group -lpthread \
  && { /stratus-embed || true; } 2>&1 | grep -q usage

# ONNX Runtime is MIT and what it links is under its own notices, which a
# binary built from them carries as text beside it.
RUN mkdir /licenses && cp LICENSE /licenses/onnxruntime-LICENSE \
 && cp ThirdPartyNotices.txt /licenses/onnxruntime-ThirdPartyNotices.txt

FROM scratch
COPY --from=build /stratus-embed /stratus-embed
COPY --from=build /licenses /licenses
