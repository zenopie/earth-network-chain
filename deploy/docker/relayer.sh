#!/bin/sh
# IBC relayer entrypoint. Off unless ENABLED=true.
#
# A relayer is trustless: it cannot forge packets or move anyone's funds, it only
# submits proofs that both chains verify for themselves. The key it holds pays
# gas and nothing else, which is why this can share a deployment with the node
# without the concerns that surround the consensus key. The worst a broken or
# absent relayer does is delay packets until they time out and refund.
#
# What it CANNOT do is run without funds. Earth is zero-fee, but every packet
# also needs a transaction on the counterparty, paid in that chain's token. A
# relayer whose counterparty balance runs dry stops relaying silently.
set -eu

say() { printf '[relayer] %s\n' "$*"; }

if [ "${ENABLED:-false}" != "true" ]; then
  say "ENABLED is not 'true' — relayer is off"
  say "set ENABLED=true plus COUNTERPARTY_* and RELAYER_MNEMONIC to turn it on"
  # Idle rather than exit. Exiting would put the service in a crash-loop that
  # looks like a failure in the deployment's status, when being off is intended.
  while true; do sleep 3600; done
fi

: "${COUNTERPARTY_CHAIN_ID:?set COUNTERPARTY_CHAIN_ID when ENABLED=true}"
: "${COUNTERPARTY_RPC:?set COUNTERPARTY_RPC when ENABLED=true}"
: "${RELAYER_MNEMONIC:?set RELAYER_MNEMONIC when ENABLED=true}"

CHAIN_ID="${CHAIN_ID:-earth-1}"
EARTH_RPC="${EARTH_RPC:-http://node:26657}"
COUNTERPARTY_PREFIX="${COUNTERPARTY_PREFIX:-cosmos}"
COUNTERPARTY_GAS_PRICES="${COUNTERPARTY_GAS_PRICES:-0.025uatom}"
PATH_NAME="${PATH_NAME:-earth-hub}"
RLY_HOME="${RLY_HOME:-/data/relayer}"

mkdir -p "$RLY_HOME"

if [ ! -f "$RLY_HOME/config/config.yaml" ]; then
  say "initialising relayer config at $RLY_HOME"
  rly config init --home "$RLY_HOME"

  # coin-type 118 on both sides: Earth uses the Cosmos default (app/app.go).
  # Getting it wrong yields addresses that look correct and hold nothing.
  cat > /tmp/earth.json <<EOF
{"type":"cosmos","value":{
  "key":"default","chain-id":"$CHAIN_ID","rpc-addr":"$EARTH_RPC",
  "account-prefix":"earth","keyring-backend":"test",
  "gas-adjustment":1.5,"gas-prices":"0uerth","min-gas-amount":0,
  "debug":false,"timeout":"20s","output-format":"json",
  "sign-mode":"direct","extra-codecs":[],"coin-type":118,
  "broadcast-mode":"batch"}}
EOF
  cat > /tmp/counterparty.json <<EOF
{"type":"cosmos","value":{
  "key":"default","chain-id":"$COUNTERPARTY_CHAIN_ID","rpc-addr":"$COUNTERPARTY_RPC",
  "account-prefix":"$COUNTERPARTY_PREFIX","keyring-backend":"test",
  "gas-adjustment":1.5,"gas-prices":"$COUNTERPARTY_GAS_PRICES","min-gas-amount":0,
  "debug":false,"timeout":"20s","output-format":"json",
  "sign-mode":"direct","extra-codecs":[],"coin-type":118,
  "broadcast-mode":"batch"}}
EOF
  rly chains add --file /tmp/earth.json "$CHAIN_ID" --home "$RLY_HOME"
  rly chains add --file /tmp/counterparty.json "$COUNTERPARTY_CHAIN_ID" --home "$RLY_HOME"

  # One mnemonic, both chains. Same key material, different prefixes.
  rly keys restore "$CHAIN_ID" default "$RELAYER_MNEMONIC" --home "$RLY_HOME"
  rly keys restore "$COUNTERPARTY_CHAIN_ID" default "$RELAYER_MNEMONIC" --home "$RLY_HOME"

  rly paths new "$CHAIN_ID" "$COUNTERPARTY_CHAIN_ID" "$PATH_NAME" --home "$RLY_HOME"
  say "config written — the path has no channel yet"
fi

# Creating the client/connection/channel is a one-off that spends gas on both
# chains, so it is opt-in rather than something a restart re-attempts.
if [ "${LINK_ON_START:-false}" = "true" ]; then
  say "linking $PATH_NAME (creates client, connection and channel)"
  rly transact link "$PATH_NAME" --home "$RLY_HOME"
fi

say "relaying $PATH_NAME"
exec rly start "$PATH_NAME" --home "$RLY_HOME"
