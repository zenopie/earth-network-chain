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
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates jq \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL "https://github.com/ignite/cli/releases/download/v${IGNITE_VERSION}/ignite_${IGNITE_VERSION}_linux_amd64.tar.gz" \
        | tar -xz -C /usr/local/bin ignite \
    && ignite version

WORKDIR /src
COPY go.mod go.sum ./
# Warm the module cache so first boot is not also a cold dependency download.
RUN go mod download

COPY . .
# Prove the tree builds at image-build time rather than discovering it on a node.
RUN go build -o /usr/local/bin/earthd ./cmd/earthd

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
