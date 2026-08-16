# Dockerfile — single-validator earth node for SecretVM.
#
# The image carries the Go toolchain, Ignite and the source tree rather than just
# the compiled binary, because genesis is built at first boot with
# `ignite chain init`. That is deliberate:
#
#   config.yml is the source of truth for genesis — 539 CSCAs, the seven register
#   verifying keys, the seeded ANML/ERTH pool, the governance parameters. Baking
#   a genesis at build time would mean also baking the validator's consensus key
#   (the gentx is bound to it), putting a signing key in a public image. The
#   alternative, reassembling that state in an entrypoint, means hand-splicing
#   bank balances and supply totals — the exact class of change that has broken a
#   running chain here before while passing every test.
#
# So: fresh keys per deployment, no secrets in the image, and a genesis produced
# by the same path used locally. The cost is a large image, which for a devnet is
# the cheaper mistake.

# trixie, not bookworm, for the compiler: Aztec's C++20 headers do not compile
# with bookworm's default clang 14 (it rejects the constexpr constructors in
# field2_declarations.hpp). verifier-libs.yml builds these libs on ubuntu-latest,
# which is clang 18; trixie's default is close to that.
#
# The platform is the builder's, not a constant. CI runs on amd64 and SecretVM's
# TDX hosts are amd64, so that is what ships; leaving it unpinned also means an
# arm64 machine builds a working arm64 image instead of an emulated x86 one that
# dies with SIGILL the moment Barretenberg hits a vector instruction.
FROM golang:1.25-trixie

# Ignite needs Go >= 1.25.10 on PATH; the base image supplies it.
#
# Installed from the GitHub release tarball rather than get.ignite.com: that
# installer answers with an HTML page, which bash then tries to execute
# ("syntax error near unexpected token `newline'"). The version has no leading
# "v" in the asset name but does in the tag.
#
# curl -f and tar already fail loudly on a bad URL or a changed archive layout,
# so there is no separate version check here: running the binary at build time
# only proves it runs on the *builder's* architecture, which is not necessarily
# the one being built for.
ARG IGNITE_VERSION=29.10.1
# Set by BuildKit (amd64 / arm64); defaulted so a plain `docker build` without
# BuildKit still resolves.
ARG TARGETARCH=amd64
# clang/python3/binutils are for the Barretenberg verifier lib built below, not
# for Ignite.
#
# libc++ specifically, not libstdc++: build-wrapper.sh compiles the shim with
# -stdlib=libc++ to match Aztec's prebuilt archive, and the cgo LDFLAGS link
# -lc++. Without the dev packages clang cannot find <memory> and the build stops
# at the first standard header. Both are kept in the final image — this is a
# single-stage build and the binary links libc++ dynamically.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates jq clang python3 binutils \
        libc++-dev libc++abi-dev \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL "https://github.com/ignite/cli/releases/download/v${IGNITE_VERSION}/ignite_${IGNITE_VERSION}_linux_${TARGETARCH}.tar.gz" \
        | tar -xz -C /usr/local/bin ignite

WORKDIR /src
COPY go.mod go.sum ./
# go.mod replaces github.com/burnt-labs/barretenberg-go with ./third_party, so
# resolution needs that module's go.mod present before the rest of the tree is
# copied — otherwise this layer fails with "no such file or directory" on it.
# Only the go.mod is copied, so the layer still caches on dependency changes
# rather than on every source edit.
COPY third_party/barretenberg-go/go.mod third_party/barretenberg-go/
# Warm the module cache so first boot is not also a cold dependency download.
RUN go mod download

COPY . .

# Build the native UltraHonk verifier lib for this platform.
#
# Only lib/darwin_arm64 is checked into the repo, for local development; the
# Linux libs are produced by .github/workflows/verifier-libs.yml and attached to
# a release. Building it here instead keeps the image self-contained — otherwise
# an image could only be built after a release that carries the matching lib.
#
# This is not a Barretenberg compile: the script downloads Aztec's prebuilt
# libbb-external.a at the tag pinned in checksums.json, compiles the C++ shim,
# and merges the two archives.
ARG TARGETARCH
RUN cd third_party/barretenberg-go \
    && ./scripts/build-wrapper.sh --platform "linux_${TARGETARCH}"

# Prove the tree builds at image-build time rather than discovering it on a node.
RUN CGO_ENABLED=1 go build -o /usr/local/bin/earthd ./cmd/earthd

# The verifier is CGo, so make sure the binary actually links and runs rather
# than only that it compiled.
RUN earthd version

COPY deploy/secretvm/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Node home. Mount a volume here — without one, every redeploy is a brand new
# chain with a new genesis, new keys and no history.
ENV EARTH_HOME=/data
VOLUME ["/data"]

# LCD and RPC are what the compose publishes. gRPC (9090) and p2p (26656) are
# listening too — the entrypoint binds them — but nothing reaches for them: the
# backend and both wallet apps speak REST, and a single validator has no peers.
EXPOSE 1317 26657

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
