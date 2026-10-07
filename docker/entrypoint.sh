#!/usr/bin/env bash
#
# The image's entrypoint: a node on the release genesis, and nothing else.
#
#   first start   $EARTH_HOME has no config/genesis.json: check the genesis
#                 baked into the image against the sha256 published with it,
#                 `earthd init`, install that genesis, start. No key is
#                 imported and none is made beyond the random ones init
#                 writes, and the genesis is not modified, so every node that
#                 boots this image computes the same genesis and app hash.
#   later starts  start on what the volume holds.
#
# Everything else is earthd's own configuration, which it reads from
# $EARTH_HOME/config (app.toml, config.toml), from EARTHD_* environment
# variables (`.` and `-` in a key become `_`: EARTHD_MINIMUM_GAS_PRICES,
# EARTHD_PRUNING, EARTHD_RPC_LADDR, ...), and from flags appended to the
# container's command, which are passed through to `earthd start`. What an
# operator should set is in docker/README.md, "Running a node".
#
# Key handling, supervisors (cosmovisor), relayers and anything about where
# the node is hosted are the operator's: build them on top of this image.
set -euo pipefail

EARTH_HOME="${EARTH_HOME:-/data}"
MONIKER="${MONIKER:-earth-node}"
# Overridable only so docker/entrypoint_test.sh can run without a container.
GENESIS_SRC="${GENESIS_SRC:-/etc/earth/genesis.json}"
GENESIS_SHA="${GENESIS_SHA:-${GENESIS_SRC}.sha256}"

say() { printf '[entrypoint] %s\n' "$*"; }
die() { printf '[entrypoint] FATAL: %s\n' "$*" >&2; exit 1; }
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

if [ ! -f "$EARTH_HOME/config/genesis.json" ]; then
  [ -f "$GENESIS_SRC" ] || die "$GENESIS_SRC is missing from the image"
  [ -f "$GENESIS_SHA" ] || die "$GENESIS_SHA is missing from the image"
  want="$(awk '{print $1}' "$GENESIS_SHA")"
  got="$(sha256_of "$GENESIS_SRC")"
  [ "$want" = "$got" ] || die "genesis sha256 mismatch: refusing to start
    expected $want
    got      $got
  The genesis in this image is not the one it was built with."
  # The file is canonical JSON (keys sorted, two-space indent;
  # scripts/build-genesis.sh), so the top-level chain_id is this line.
  chain_id="$(sed -n 's/^  "chain_id": "\([^"]*\)",\{0,1\}$/\1/p' "$GENESIS_SRC" | head -1)"
  [ -n "$chain_id" ] || die "no chain_id in $GENESIS_SRC"
  earthd init "$MONIKER" --chain-id "$chain_id" --home "$EARTH_HOME" >/dev/null 2>&1 \
    || die "earthd init failed for $EARTH_HOME"
  cp "$GENESIS_SRC" "$EARTH_HOME/config/genesis.json"
  say "initialised $EARTH_HOME on $chain_id, genesis sha256 $got"
else
  on_disk="$(sha256_of "$EARTH_HOME/config/genesis.json")"
  say "resuming $EARTH_HOME, genesis sha256 $on_disk"
  if [ -f "$GENESIS_SRC" ] && [ "$on_disk" != "$(sha256_of "$GENESIS_SRC")" ]; then
    say "WARNING: that is not this image's genesis ($(sha256_of "$GENESIS_SRC")): the volume holds another chain"
  fi
fi

# A container is only useful if its RPC and LCD can be reached from outside
# it; earthd's defaults listen on loopback. Flags given to the container come
# after these and win.
exec earthd start --home "$EARTH_HOME" \
  --rpc.laddr tcp://0.0.0.0:26657 \
  --api.enable --api.address tcp://0.0.0.0:1317 \
  "$@"
