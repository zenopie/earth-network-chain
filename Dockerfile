# Dockerfile — single-validator earth node.
#
# Runs under docker-compose (plain Docker host or SecretVM) and on Akash;
# both use the same entrypoint.
#
# Two stages: build earthd, then ship it on a slim base. The runtime carries no
# Go toolchain, no Ignite and no source.
#
# Genesis comes from networks/genesis.json, generated once with `ignite chain init`
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
RUN go mod download

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

# cosmovisor supervises earthd across upgrades: the chain halts on purpose at the
# upgrade height, and cosmovisor swaps the binary and restarts it so nobody has
# to be awake for it. Pure Go, so CGO off gives a static binary with nothing to
# resolve at load time.
#
# Pinned rather than @latest. cosmovisor is the process that decides which binary
# runs the chain, so "whatever was newest that day" is not a property this image
# should have.
RUN CGO_ENABLED=0 GOBIN=/out go install cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@v1.7.1

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
COPY --from=build /out/rly /usr/local/bin/rly
COPY --from=build /out/cosmovisor /usr/local/bin/cosmovisor

# Prove the binary can actually start before the image ships. `earthd --help`
# touches no chain state, but it forces the dynamic loader to resolve every
# NEEDED entry — libwasmvm included — so a library the loader cannot find
# becomes a red build instead of a container that exits instantly on a provider
# whose logs you cannot read.
RUN earthd --help >/dev/null && echo "earthd links and runs"
RUN cosmovisor version >/dev/null 2>&1 || cosmovisor help >/dev/null
COPY docker/relayer.sh /usr/local/bin/relayer.sh
# Genesis and the hash it is checked against. The entrypoint refuses to start if
# they disagree, so a genesis swapped into the image after the fact fails loudly
# rather than quietly forking whoever runs it.
COPY networks/genesis.json /etc/earth/genesis.json
COPY networks/genesis.json.sha256 /etc/earth/genesis.json.sha256
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/relayer.sh

# Node home. Mount a volume here — without one, every redeploy is a brand new
# chain with a new genesis, new keys and no history.
ENV EARTH_HOME=/data
VOLUME ["/data"]

# LCD, RPC and p2p. p2p is what lets this node have peers at all; without it the
# container can only ever be its own network.
EXPOSE 1317 26656 26657

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
