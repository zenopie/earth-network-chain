# Dockerfile — earth node.
#
# Two stages: build earthd, then ship it on a slim base. The runtime carries no
# Go toolchain and no source.
#
# A generic image: earthd, its libraries, the release genesis and a minimal
# entrypoint (docker/entrypoint.sh: install the genesis on a fresh home, then
# `earthd start`). An operator's key handling, supervisor or relayer belong in
# an image built FROM this one by digest (docker/README.md).
#
# Genesis is networks/genesis.json, built by `make genesis` from
# networks/genesis/ and committed with its sha256: the CSCAs, the passport
# register and privacy verifying keys, the seeded ANML/ERTH pool, the
# governance parameters and the genesis validator's gentx.

# ---- build ----------------------------------------------------------------
# trixie for the compiler: Aztec's C++20 headers do not compile with bookworm's
# clang 14, which rejects the constexpr constructors in field2_declarations.hpp.
# Pinned to a patch release, not the floating 1.25 tag: the compiler version is
# an input to the binary, and "whatever 1.25 resolves to today" is not a
# reproducible input. Bump deliberately.
FROM golang:1.25.10-trixie AS build

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
# Go's module proxy and checksum database are a network dependency of every build
# here, and they fail intermittently: two consecutive v0.4.8 builds died with
#
#   stream error: stream ID 71; INTERNAL_ERROR; received from peer
#
# once on proxy.golang.org during `go mod download` and once on sum.golang.org
# while installing a Go tool. Nothing was wrong with either build.
#
# Retry rather than weaken verification. GONOSUMDB or GOFLAGS=-insecure would
# also make these pass, by skipping the checksums that make a supply-chain
# substitution visible -- a far worse trade than waiting fifteen seconds.
#
# The trailing test is load-bearing: a bare `for ... done` exits 0 even when
# every attempt failed, which would bake a half-populated module cache into the
# image and fail later somewhere less obvious.
RUN for i in 1 2 3; do go mod download && ok=1 && break; echo "go mod download failed (attempt $i)"; sleep 15; done; [ "${ok:-}" = 1 ]

COPY . .

# libwasmvm — the Rust engine behind x/wasm, linked through cgo.
#
# It is a prebuilt shared object inside the wasmvm Go module rather than
# something compiled here, so the only work is putting it where the linker and,
# later, the runtime image can find it. Both halves matter: with the .so present
# at build time but missing at runtime, earthd builds cleanly and then dies on
# startup with "libwasmvm.so: cannot open shared object file", which reads like
# a corrupt image rather than a missing dependency.
#
# The path comes from `go list` rather than being written out, because the module
# cache escapes capitals ("CosmWasm" becomes "!cosm!wasm") and the version is
# pinned in go.mod, not here. TARGETARCH is Docker's (amd64/arm64); the library
# is named for the machine architecture (x86_64/aarch64), hence uname.
RUN cp "$(go list -m -f '{{.Dir}}' github.com/CosmWasm/wasmvm/v3)/internal/api/libwasmvm.$(uname -m).so" \
        /usr/local/lib/libwasmvm.$(uname -m).so \
    && ldconfig /usr/local/lib

# Build the native UltraHonk verifier lib. Only lib/darwin_arm64 is checked in;
# verifier-libs.yml produces the Linux ones for releases. Building it here keeps
# the image self-contained. This is not a Barretenberg compile — the script
# fetches Aztec's prebuilt archive at the tag pinned in checksums.json, compiles
# the shim and merges them.
ARG TARGETARCH
RUN cd third_party/barretenberg-go \
    && ./scripts/build-wrapper.sh --platform "linux_${TARGETARCH:-amd64}"

# -trimpath and pinned ldflags, both for the same reason: two operators building
# this image must get the same binary. Without -trimpath the build embeds absolute
# source paths, so the output differs by where it was checked out. Without the
# ldflags `earthd version` reports nothing, which is the only way an operator can
# answer "am I running what everyone else is running" during an upgrade.
#
# VERSION and COMMIT are build args rather than derived from git, because .git is
# not in the build context and a value invented here would be a lie.
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=1 go build -trimpath \
        -ldflags "-X github.com/cosmos/cosmos-sdk/version.Name=earth \
                  -X github.com/cosmos/cosmos-sdk/version.AppName=earthd \
                  -X github.com/cosmos/cosmos-sdk/version.Version=${VERSION} \
                  -X github.com/cosmos/cosmos-sdk/version.Commit=${COMMIT}" \
        -o /out/earthd ./cmd/earthd

# ---- runtime --------------------------------------------------------------
FROM debian:trixie-slim

# libc++1/libc++abi1 are the runtime halves of what the verifier links against;
# without them earthd will not start.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libc++1 libc++abi1 \
    && rm -rf /var/lib/apt/lists/*

# libwasmvm is dynamically linked, so it has to ship with the binary. Copied
# under a glob because the file is named for the architecture and this stage has
# no shell expansion of `uname` in a COPY.
COPY --from=build /usr/local/lib/libwasmvm.*.so /usr/local/lib/
RUN ldconfig /usr/local/lib

COPY --from=build /out/earthd /usr/local/bin/earthd

# Prove the binary can actually start before the image ships. `earthd --help`
# touches no chain state, but it forces the dynamic loader to resolve every
# NEEDED entry — libwasmvm included — so a library the loader cannot find
# becomes a red build instead of a container that exits instantly on a host
# whose logs you cannot read.
RUN earthd --help >/dev/null && echo "earthd links and runs"
# Genesis and the hash it is checked against. The entrypoint refuses to start if
# they disagree, so a genesis swapped into the image after the fact fails loudly
# rather than quietly forking whoever runs it.
COPY networks/genesis.json /etc/earth/genesis.json
COPY networks/genesis.json.sha256 /etc/earth/genesis.json.sha256
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh

# The node runs as `earth`, not root: earthd executes native code on input
# strangers choose (the proof verifier, the CosmWasm engine). /data is created
# owned by it, so a named volume mounted there starts out writable; a volume
# owned by someone else needs its ownership fixed by whoever mounts it.
RUN useradd --system --uid 10001 --home-dir /data --no-create-home --shell /usr/sbin/nologin earth \
    && mkdir -p /data && chown earth:earth /data

# Node home. Mount a volume here — without one, every restart is a brand new
# node with new keys and no history.
ENV EARTH_HOME=/data HOME=/data
VOLUME ["/data"]
USER earth

# LCD, RPC and p2p. p2p is what lets this node have peers at all; without it the
# container can only ever be its own network.
EXPOSE 1317 26656 26657

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
