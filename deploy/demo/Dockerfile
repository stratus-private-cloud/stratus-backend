# The demo's image: the published one, unchanged underneath, with what it
# takes to seed itself every time it starts.
#
# Its data is in /dev/shm (see fly.toml), so a machine that restarts comes back
# empty -- Fly restarting it, moving it to another host, or the hourly reset
# doing it on purpose. Seeding from outside meant something outside noticing,
# and GitHub's schedule runs when it likes; seeding from inside needs a shell,
# curl and GNU date, which is what scripts/seed-demo.sh is written in and what
# the published image deliberately does not have. So they are laid over it
# here, for this instance alone. The binary, ffmpeg, ffprobe and the user are
# the published image's own, byte for byte: what the demo proves is still the
# build, while scripts/smoke.sh goes on asserting the image people pull has no
# shell in it.
#
# Built from the root of the repository:
#
#   docker build -f deploy/demo/Dockerfile --build-arg IMAGE=<digest> .

ARG IMAGE=ghcr.io/stratus-private-cloud/stratus-backend:main
ARG ALPINE_VERSION=3.24

# The media goes in with the image rather than being fetched at start, so a
# machine that restarts reseeds itself with no route out, and the checksum the
# repository pins is checked once, here.
FROM alpine:${ALPINE_VERSION} AS media
# hadolint ignore=DL3018
RUN apk add --no-cache curl
WORKDIR /media
COPY scripts/demo/SHA256SUMS SHA256SUMS
RUN curl -fsSLO https://github.com/stratus-private-cloud/stratus-backend/releases/download/demo-media-v2/demo-media-v2.tar.gz && \
    sha256sum -c SHA256SUMS

# Alpine's own tree laid over distroless, rather than static builds picked one
# by one: bash, curl and coreutils are dynamically linked against musl, and
# copying /lib and the two bin directories is what brings their libraries with
# them. COPY merges into what distroless has there and touches nothing under
# /usr/local, where the server and the media tools are.
FROM alpine:${ALPINE_VERSION} AS tools
# hadolint ignore=DL3018
RUN apk add --no-cache bash curl coreutils

# hadolint ignore=DL3006
FROM ${IMAGE}
COPY --from=tools /lib /lib
COPY --from=tools /usr/lib /usr/lib
COPY --from=tools /bin /bin
COPY --from=tools /usr/bin /usr/bin
COPY --from=media /media/demo-media-v2.tar.gz /demo/demo-media-v2.tar.gz
COPY scripts/demo/SHA256SUMS /demo/demo/SHA256SUMS
COPY scripts/seed-demo.sh /demo/seed-demo.sh
COPY deploy/demo/entrypoint.sh /demo/entrypoint.sh
ENTRYPOINT ["/demo/entrypoint.sh"]
