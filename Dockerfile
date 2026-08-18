# Dockerfile — single-validator earth node AND the ads-for-gas service.
#
# One image, two entrypoints. The Akash SDL chooses: the default runs the node,
# /usr/local/bin/backend-entrypoint.sh runs the FastAPI service.
#
# Runs under docker-compose (plain Docker host or SecretVM) and on Akash;
# both use the same entrypoint.
#
# Two stages: build earthd, then ship it on a slim base. The runtime carries no
# Go toolchain, no Ignite and no source.
#
# Genesis comes from deploy/genesis.json, generated once with `ignite chain init`
# and committed. It holds everything config.yml describes — the 539 CSCAs, the
# seven register verifying keys, the seeded ANML/ERTH pool, the governance
# parameters — with the gentx and the dev accounts stripped, so no key that only
# ever existed on a developer's machine is baked into a public image. The
# entrypoint creates the validator with stock earthd genesis commands.
#
# An earlier version ran `ignite chain init` inside the container instead. That
# cannot work against a mounted volume: init removes and recreates the home
# directory, and /data is a mount point, so it fails with
#
#     Unlinkat //data: device or resource busy
#
# It also made the image multi-gigabyte, which is what made pushing it to ghcr
# flaky.

# ---- build ----------------------------------------------------------------
# trixie for the compiler: Aztec's C++20 headers do not compile with bookworm's
# clang 14, which rejects the constexpr constructors in field2_declarations.hpp.
FROM golang:1.25-trixie AS build

# libc++ specifically, not libstdc++: build-wrapper.sh compiles the verifier shim
# with -stdlib=libc++ to match Aztec's prebuilt archive, and the cgo LDFLAGS link
# -lc++.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates clang python3 binutils \
        libc++-dev libc++abi-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
# go.mod replaces github.com/burnt-labs/barretenberg-go with ./third_party, so
# resolution needs that module's go.mod before the rest of the tree is copied.
COPY third_party/barretenberg-go/go.mod third_party/barretenberg-go/
RUN go mod download

COPY . .

# Build the native UltraHonk verifier lib. Only lib/darwin_arm64 is checked in;
# verifier-libs.yml produces the Linux ones for releases. Building it here keeps
# the image self-contained. This is not a Barretenberg compile — the script
# fetches Aztec's prebuilt archive at the tag pinned in checksums.json, compiles
# the shim and merges them.
ARG TARGETARCH
RUN cd third_party/barretenberg-go \
    && ./scripts/build-wrapper.sh --platform "linux_${TARGETARCH:-amd64}"

RUN CGO_ENABLED=1 go build -o /out/earthd ./cmd/earthd

# ---- runtime --------------------------------------------------------------
FROM debian:trixie-slim

# libc++1/libc++abi1 are the runtime halves of what the verifier links against;
# without them earthd will not start.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libc++1 libc++abi1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/earthd /usr/local/bin/earthd
COPY deploy/genesis.json /etc/earth/genesis.json
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY deploy/docker/backend-entrypoint.sh /usr/local/bin/backend-entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/backend-entrypoint.sh

# ---- ads-for-gas ----------------------------------------------------------
# The Python service ships in the same image as the chain. One build, one
# digest, one thing to pin; the SDL picks which of the two entrypoints to run.
# The cost is that each pod carries the other's payload, which is why the
# backend's root storage in its SDL is sized for this image rather than for a
# slim Python one.
#
# A venv rather than a bare `pip install`: Debian's python3 is externally
# managed (PEP 668) and refuses to be installed into directly.
#
# build-essential goes in and comes out in the same layer, as the backend's own
# Dockerfile did — cosmpy's crypto dependencies fall back to building from
# source when there is no wheel for the platform, and a failure there would
# otherwise only show up in CI.
COPY backend /app/backend
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-venv build-essential \
    && python3 -m venv /opt/venv \
    && /opt/venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/venv/bin/pip install --no-cache-dir -r /app/backend/requirements.txt \
    && apt-get purge -y --auto-remove build-essential \
    && rm -rf /var/lib/apt/lists/*

# Replay protection for SSV transaction ids. Mount a volume here when running
# the backend: a fresh filesystem forgets which ids were honoured, and every one
# of them becomes replayable against the hot wallet.
ENV STATE_DB=/app/state/ads_for_gas.db
RUN mkdir -p /app/state

# Node home. Mount a volume here — without one, every redeploy is a brand new
# chain with a new genesis, new keys and no history.
ENV EARTH_HOME=/data
VOLUME ["/data"]

# LCD and RPC. gRPC and p2p are not published: everything here speaks REST, and
# a single validator has no peers.
# 1317/26657 are the chain; 8000 is the ads-for-gas service.
EXPOSE 1317 26657 8000

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
