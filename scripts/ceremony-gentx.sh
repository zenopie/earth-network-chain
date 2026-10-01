#!/usr/bin/env bash
#
# Genesis ceremony: regenerate networks/genesis/gentx/genesis-validator.json
# with a NEW validator consensus key.
#
# The privacy relaunch keeps chain-id earth-1 (networks/genesis/chain.json)
# but must not reuse the old chain's consensus key: a key that signed earth-1
# blocks before could double-sign the same heights on the new chain. The
# gentx committed today still carries the old devnet consensus pubkey and is a
# placeholder. TODO(ceremony): run this, on the operator's machine, before the
# launch genesis is published.
#
#   VALIDATOR_MNEMONIC='...' ./scripts/ceremony-gentx.sh [--pubkey '<consensus pubkey json>']
#
# The operator key (VALIDATOR_MNEMONIC, never in this repo) must be the
# genesis account in networks/genesis/accounts.json; it signs the gentx. The
# consensus key is generated fresh in a temporary home, or, with --pubkey, is
# the remote signer's (as `earthd comet show-validator` prints it) and never
# touches this machine. The self-delegation, moniker and commission are read
# from the gentx being replaced, so only the keys change.
#
# Afterwards: move the printed priv_validator_key.json to the validator (or
# discard it with --pubkey), then `make genesis && make genesis-check` and
# commit the gentx with the rebuilt genesis.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$REPO/networks/genesis"
GENTX="$SRC/gentx/genesis-validator.json"
PUBKEY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --pubkey) PUBKEY="$2"; shift 2 ;;
    -h|--help) sed -n '2,27p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
: "${VALIDATOR_MNEMONIC:?set VALIDATOR_MNEMONIC to the operator mnemonic of the genesis validator}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CHAIN_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["chain_id"])' "$SRC/chain.json")"
FIELDS=$(python3 -c '
import json, sys
m = json.load(open(sys.argv[1]))["body"]["messages"][0]
v, c = m["value"], m["commission"]
print(m["description"]["moniker"], v["amount"] + v["denom"], c["rate"], c["max_rate"], c["max_change_rate"], m["min_self_delegation"])
' "$GENTX")
read -r MONIKER AMOUNT RATE MAXRATE MAXCHANGE MINSELF <<<"$FIELDS"

go build -o "$WORK/earthd" "$REPO/cmd/earthd"
H="$WORK/home"
"$WORK/earthd" init "$MONIKER" --chain-id "$CHAIN_ID" --home "$H" >/dev/null 2>&1   # fresh consensus key
cp "$REPO/networks/genesis.json" "$H/config/genesis.json"
echo "$VALIDATOR_MNEMONIC" | "$WORK/earthd" keys add operator --recover --keyring-backend test --home "$H" >/dev/null 2>&1

args=(genesis gentx operator "$AMOUNT" --chain-id "$CHAIN_ID" --moniker "$MONIKER"
  --commission-rate "$RATE" --commission-max-rate "$MAXRATE" --commission-max-change-rate "$MAXCHANGE"
  --min-self-delegation "$MINSELF" --keyring-backend test --home "$H" --output-document "$WORK/gentx.json")
[ -n "$PUBKEY" ] && args+=(--pubkey "$PUBKEY")
"$WORK/earthd" "${args[@]}" >/dev/null 2>&1

python3 - "$WORK/gentx.json" "$GENTX" <<'PY'
import json, sys
new, old = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
n, o = new['body']['messages'][0], old['body']['messages'][0]
if n['validator_address'] != o['validator_address']:
    sys.exit('the mnemonic does not belong to the genesis validator: %s != %s' % (n['validator_address'], o['validator_address']))
if n['pubkey'] == o['pubkey']:
    sys.exit('consensus key unchanged: refusing to reuse the old chain key')
PY
cp "$WORK/gentx.json" "$GENTX"
NEWKEY=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["body"]["messages"][0]["pubkey"]["key"])' "$GENTX")
echo "wrote $GENTX (new consensus pubkey $NEWKEY)"
if [ -z "$PUBKEY" ]; then
  mkdir -p "$REPO/.ceremony" && cp "$H/config/priv_validator_key.json" "$REPO/.ceremony/priv_validator_key.json"
  chmod 600 "$REPO/.ceremony/priv_validator_key.json"
  echo "new consensus key: $REPO/.ceremony/priv_validator_key.json — move it to the validator, then delete it here"
fi
echo "next: make genesis && make genesis-check"
