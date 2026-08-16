#!/usr/bin/env bash
#
# Starts a single-validator earth node, initialising genesis on first boot.
#
# State lives entirely under $EARTH_HOME, which is a mounted volume. The genesis
# check below is what makes a redeploy resume the existing chain instead of
# silently starting a new one with new keys.
set -euo pipefail

EARTH_HOME="${EARTH_HOME:-/data}"
MONIKER="${MONIKER:-earth-secretvm}"
MIN_GAS_PRICES="${MIN_GAS_PRICES:-0uerth}"

say() { printf '[entrypoint] %s\n' "$*"; }

if [ ! -f "$EARTH_HOME/config/genesis.json" ]; then
  say "no genesis at $EARTH_HOME — initialising a fresh chain from config.yml"
  # Builds the binary and lays down genesis for one validator, seeded with
  # everything config.yml declares: the CSCA trust store, the register verifying
  # keys, the ANML/ERTH pool, governance parameters.
  ignite chain init --home "$EARTH_HOME" --skip-proto
  say "genesis ready"
  say "dev account keys are in $EARTH_HOME (test keyring); export them with:"
  say "  earthd keys export alice --keyring-backend test --home $EARTH_HOME"
else
  say "existing genesis found — resuming chain at $EARTH_HOME"
fi

# Bind to every interface. The defaults listen on loopback, which inside a
# container means nothing outside it can reach the node — including the
# ads-for-gas backend and the wallet apps.
#
# CORS is opened because the web app talks to the LCD directly from a browser.
# That is a devnet posture: it lets any origin read and broadcast.
exec earthd start \
  --home "$EARTH_HOME" \
  --moniker "$MONIKER" \
  --minimum-gas-prices "$MIN_GAS_PRICES" \
  --rpc.laddr tcp://0.0.0.0:26657 \
  --p2p.laddr tcp://0.0.0.0:26656 \
  --grpc.address 0.0.0.0:9090 \
  --api.enable \
  --api.address tcp://0.0.0.0:1317 \
  --api.enabled-unsafe-cors \
  "$@"
