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

# Pinned to amd64 to match the Ignite tarball fetched below — and SecretVM's TDX
# hosts are amd64 anyway. Without the pin an arm64 builder would install an
# x86 ignite binary into an arm image.
FROM --platform=linux/amd64 golang:1.25-bookworm

# Ignite needs Go >= 1.25.10 on PATH; the base image supplies it.
#
# Installed from the GitHub release tarball rather than get.ignite.com: that
# installer answers with an HTML page, which bash then tries to execute
# ("syntax error near unexpected token `newline'"). The version has no leading
# "v" in the asset name but does in the tag.
#
# `ignite version` at the end is a build-time smoke check — without it a wrong
# URL or a changed archive layout only surfaces on the node at first boot.
ARG IGNITE_VERSION=29.10.1
# clang/python3/binutils are for the Barretenberg verifier lib built below, not
# for Ignite.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates jq clang python3 binutils \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL "https://github.com/ignite/cli/releases/download/v${IGNITE_VERSION}/ignite_${IGNITE_VERSION}_linux_amd64.tar.gz" \
        | tar -xz -C /usr/local/bin ignite \
    && ignite version

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
RUN cd third_party/barretenberg-go \
    && ./scripts/build-wrapper.sh --platform linux_amd64

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
