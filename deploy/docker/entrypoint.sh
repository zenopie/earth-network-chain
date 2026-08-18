#!/usr/bin/env bash
#
# Starts a single-validator earth node, initialising on first boot.
#
# Only stock earthd is used here. deploy/genesis.json carries the custom genesis
# state that config.yml describes — the CSCA trust store, the register verifying
# keys, the seeded ANML/ERTH pool, the governance parameters — with no gentx and
# no dev accounts, so the validator is created fresh on this machine and its key
# never leaves the volume.
#
# State lives entirely under $EARTH_HOME, which is a mounted volume. The genesis
# check is what makes a redeploy resume the existing chain rather than silently
# starting a new one.
set -euo pipefail

EARTH_HOME="${EARTH_HOME:-/data}"
CHAIN_ID="${CHAIN_ID:-earth-1}"
MONIKER="${MONIKER:-earth-node}"
MIN_GAS_PRICES="${MIN_GAS_PRICES:-0uerth}"
# What the validator account holds, and how much of it is bonded. The default
# bond matches config.yml's genesis validator.
VALIDATOR_COINS="${VALIDATOR_COINS:-1000000000000uerth}"
VALIDATOR_BONDED="${VALIDATOR_BONDED:-100000000uerth}"
KEYRING="--keyring-backend test --home $EARTH_HOME"

say() { printf '[entrypoint] %s\n' "$*"; }

if [ ! -f "$EARTH_HOME/config/genesis.json" ]; then
  say "no genesis at $EARTH_HOME — initialising a fresh chain"
  earthd init "$MONIKER" --chain-id "$CHAIN_ID" --home "$EARTH_HOME" >/dev/null 2>&1

  # Replace the stock genesis with the one carrying this chain's state.
  cp /etc/earth/genesis.json "$EARTH_HOME/config/genesis.json"

  # Stamp genesis_time to now.
  #
  # The committed genesis carries the timestamp of whichever machine ran
  # `ignite chain init`, and CometBFT gives block 1 exactly that time while
  # block 2 gets the wall clock. The emission is prorated against elapsed time,
  # so the whole gap is paid out in a single block: a genesis committed a day
  # ago mints a day of ERTH at height 2, and the lump grows for as long as the
  # file sits in the repo unchanged.
  NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  sed -i "s|\"genesis_time\":[[:space:]]*\"[^\"]*\"|\"genesis_time\": \"$NOW\"|" \
    "$EARTH_HOME/config/genesis.json"
  say "genesis_time stamped to $NOW"

  if [ -n "${VALIDATOR_MNEMONIC:-}" ]; then
    say "recovering the validator key from VALIDATOR_MNEMONIC"
    printf '%s\n' "$VALIDATOR_MNEMONIC" | earthd keys add validator --recover $KEYRING >/dev/null
  else
    say "generating a validator key — the mnemonic is printed once, below"
    earthd keys add validator $KEYRING --output json | tee /dev/stderr >/dev/null
    say "store that mnemonic: it is the only copy outside this volume"
  fi

  # add-genesis-account updates auth, bank balances and supply together, which
  # is why this is done with earthd rather than by editing the file.
  earthd genesis add-genesis-account validator "$VALIDATOR_COINS" $KEYRING
  earthd genesis gentx validator "$VALIDATOR_BONDED" --chain-id "$CHAIN_ID" $KEYRING >/dev/null
  earthd genesis collect-gentxs --home "$EARTH_HOME" >/dev/null 2>&1
  say "genesis ready: validator bonded $VALIDATOR_BONDED"
else
  say "existing genesis found — resuming chain at $EARTH_HOME"
fi

# Remote signer, if one is configured.
#
# PRIV_VALIDATOR_LADDR turns the node into something that asks for signatures
# rather than something that can produce them: CometBFT listens on this address
# and an external signer (tmkms) dials in, signs, and returns just the
# signature. The consensus key then lives wherever the signer runs — which is
# the point, because this container runs on hardware someone else owns.
#
# Set in config.toml rather than passed as a flag: it is a config field, and
# writing it here means a restart cannot quietly fall back to the local key.
#
# The signer is the client, so it needs no inbound address of its own. That is
# what makes a home machine on a dynamic IP a viable place to keep the key.
#
# NOTE: setting this does not delete priv_validator_key.json. A real migration
# removes it from this host afterwards — leaving it behind means the key you
# just moved is still sitting on the machine you moved it off.
if [ -n "${PRIV_VALIDATOR_LADDR:-}" ]; then
  sed -i "s|^priv_validator_laddr = .*|priv_validator_laddr = \"$PRIV_VALIDATOR_LADDR\"|" \
    "$EARTH_HOME/config/config.toml"
  say "remote signer expected at $PRIV_VALIDATOR_LADDR"
fi

# Bind to every interface. The defaults listen on loopback, which inside a
# container means nothing outside it can reach the node — including the
# ads-for-gas backend and the wallet apps.
#
# CORS is open because the web app talks to the LCD straight from a browser.
# That is a devnet posture: any origin can read and broadcast.
exec earthd start \
  --home "$EARTH_HOME" \
  --moniker "$MONIKER" \
  --minimum-gas-prices "$MIN_GAS_PRICES" \
  --rpc.laddr tcp://0.0.0.0:26657 \
  --p2p.laddr tcp://0.0.0.0:26656 \
  --api.enable \
  --api.address tcp://0.0.0.0:1317 \
  --api.enabled-unsafe-cors \
  "$@"
