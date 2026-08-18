# Dockerfile — single-validator earth node.
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

# The IBC relayer ships in the same image rather than its own. One image means
# one digest for CI to pin and one artefact to reason about, and the relayer is
# inert unless the SDL turns it on. Installed from the module root: the
# .../cmd/rly package path no longer exists, and the binary comes out named
# `relayer`.
# CGO_ENABLED=1, not 0. With cgo off, go-ethereum compiles signature_nocgo.go,
# which calls btc_ecdsa.SignCompact with the wrong arity for the btcec version
# this dependency graph resolves to:
#
#   signature_nocgo.go:85: assignment mismatch: 2 variables but
#   btc_ecdsa.SignCompact returns 1 value
#
# The cgo path sidesteps it entirely. The runtime image is debian and already
# carries glibc for earthd, so a dynamically linked relayer runs there fine.
RUN CGO_ENABLED=1 GOBIN=/out go install github.com/cosmos/relayer/v2@v2.6.0 \
    && mv /out/relayer /out/rly

# ---- runtime --------------------------------------------------------------
FROM debian:trixie-slim

# libc++1/libc++abi1 are the runtime halves of what the verifier links against;
# without them earthd will not start.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libc++1 libc++abi1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/earthd /usr/local/bin/earthd
COPY --from=build /out/rly /usr/local/bin/rly
COPY deploy/docker/relayer.sh /usr/local/bin/relayer.sh
COPY deploy/genesis.json /etc/earth/genesis.json
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/relayer.sh

# Node home. Mount a volume here — without one, every redeploy is a brand new
# chain with a new genesis, new keys and no history.
ENV EARTH_HOME=/data
VOLUME ["/data"]

# LCD and RPC. gRPC and p2p are not published: everything here speaks REST, and
# a single validator has no peers.
EXPOSE 1317 26657

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
