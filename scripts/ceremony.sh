#!/usr/bin/env bash
#
# The launch ceremony: turn the placeholder genesis sources into the launch
# genesis, in one command, on the operator's machine.
#
#   scripts/ceremony.sh --genesis-time <RFC3339> --pubkey '<consensus pubkey json>'
#
#   --genesis-time   the launch instant, e.g. 2026-10-20T16:00:00Z (UTC, whole
#                    seconds, in the future). Written to networks/genesis/chain.json.
#   --pubkey         the validator's consensus key as `earthd comet show-validator`
#                    prints it, e.g.
#                    '{"@type":"/cosmos.crypto.ed25519.PubKey","key":"PGqv…"}'.
#                    Only the public key: the private key never touches this
#                    machine. A key that signed an earlier earth-1 is refused.
#   --env-file PATH  where VALIDATOR_MNEMONIC is (default: $EARTH_DEPLOY_ENV, else
#                    the deploy repo's .env next to this repo). Ignored when
#                    VALIDATOR_MNEMONIC is already set in the environment.
#   --memo-peer ID@HOST:PORT
#                    the gentx memo (default: the placeholder gentx's).
#   --moniker NAME   the validator's moniker (default: the placeholder gentx's).
#
# What it does, all or nothing (any failure restores every source it touched):
#
#   1. reads VALIDATOR_MNEMONIC (only that line of the .env; never printed) into
#      a throwaway test keyring and checks it is the launch operator
#      earth1n6amvkgfrrgy6ulhurewnm0endkgye69fkcapr;
#   2. networks/genesis/accounts.json: removes the devnet faucet
#      (earth1s7rgs…) and the ads-for-gas wallet (earth1jtc2z…), and swaps the
#      placeholder validator account (earth14e6s…) for the operator with the
#      same 1,000 ERTH;
#   3. networks/genesis/chain.json: genesis_time;
#   4. networks/genesis/gentx/genesis-validator.json: a new gentx signed by the
#      operator with the given consensus key (self-delegation, moniker and
#      commission kept from the placeholder);
#   5. make genesis, then make genesis-check and the genesis tests with the
#      ceremony required (EARTH_REQUIRE_CEREMONY=1 go test ./networks/).
#
# Afterwards: review `git diff`, commit the sources with networks/genesis.json
# and its .sha256, and publish the printed sha256. Re-running it on finished
# sources (same operator) is allowed: it re-signs and rebuilds.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$REPO/networks/genesis"
GENTX="$SRC/gentx/genesis-validator.json"

# The launch decisions this script carries out.
OPERATOR="earth1n6amvkgfrrgy6ulhurewnm0endkgye69fkcapr"
PLACEHOLDER="earth14e6sqtf5y7mtzwykqreewe9kg3w94t0f25d54a"
DEVNET_ACCOUNTS="earth1s7rgscltvw8v3kzhj46pptdqg843ngs7th9ywp earth1jtc2zjmmmyttdayz6aw8vfgt5qn4hg7rpxaar6"
# Consensus keys that signed an earlier earth-1: never again.
USED_CONSENSUS_KEYS="kTMzoCBEj1g2z49K1D/jxuLGrhTsnzfTx6Gf1LnBUJw="

GENESIS_TIME="" PUBKEY="" ENV_FILE="${EARTH_DEPLOY_ENV:-}" MEMO_PEER="" MONIKER=""
while [ $# -gt 0 ]; do
  case "$1" in
    --genesis-time) GENESIS_TIME="$2"; shift 2 ;;
    --pubkey) PUBKEY="$2"; shift 2 ;;
    --env-file) ENV_FILE="$2"; shift 2 ;;
    --memo-peer) MEMO_PEER="$2"; shift 2 ;;
    --moniker) MONIKER="$2"; shift 2 ;;
    -h|--help) sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
die() { echo "ceremony: $*" >&2; exit 1; }
[ -n "$GENESIS_TIME" ] || die "--genesis-time <RFC3339> is required"
[ -n "$PUBKEY" ] || die "--pubkey '<consensus pubkey json>' is required"

# ── arguments ───────────────────────────────────────────────────────────────
GENESIS_TIME="$(python3 - "$GENESIS_TIME" <<'PY'
import sys, datetime
s = sys.argv[1]
try:
    t = datetime.datetime.fromisoformat(s.replace('Z', '+00:00'))
except ValueError:
    sys.exit('ceremony: --genesis-time %r is not RFC3339' % s)
if t.tzinfo is None:
    sys.exit('ceremony: --genesis-time needs a zone (Z for UTC)')
if t.microsecond:
    sys.exit('ceremony: --genesis-time must be whole seconds')
t = t.astimezone(datetime.timezone.utc)
if t <= datetime.datetime.now(datetime.timezone.utc):
    sys.exit('ceremony: --genesis-time %s is not in the future (a stale genesis pays out the gap at height 2)' % s)
print(t.strftime('%Y-%m-%dT%H:%M:%SZ'))
PY
)" || exit 1
python3 - "$PUBKEY" "$USED_CONSENSUS_KEYS" <<'PY' || exit 1
import sys, json, base64
try:
    k = json.loads(sys.argv[1])
except ValueError:
    sys.exit('ceremony: --pubkey is not JSON')
if k.get('@type') != '/cosmos.crypto.ed25519.PubKey' or set(k) != {'@type', 'key'}:
    sys.exit('ceremony: --pubkey must be {"@type":"/cosmos.crypto.ed25519.PubKey","key":"<base64>"}')
try:
    raw = base64.b64decode(k['key'], validate=True)
except Exception:
    sys.exit('ceremony: --pubkey key is not base64')
if len(raw) != 32:
    sys.exit('ceremony: --pubkey key is %d bytes, not 32' % len(raw))
if k['key'] in sys.argv[2].split():
    sys.exit('ceremony: consensus key %s signed an earlier earth-1: refusing to reuse it' % k['key'])
PY

# ── the mnemonic: from the environment, or that one line of the .env ────────
if [ -z "${VALIDATOR_MNEMONIC:-}" ]; then
  if [ -z "$ENV_FILE" ]; then
    for c in "$REPO/../earth-network-deploy/.env" "$REPO/../../earth-network-deploy/.env"; do
      [ -f "$c" ] && { ENV_FILE="$c"; break; }
    done
  fi
  [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ] || die "no VALIDATOR_MNEMONIC in the environment and no deploy .env found (--env-file PATH)"
  # Only VALIDATOR_MNEMONIC is read; the file is not sourced (it holds other
  # secrets, and sourcing would run it).
  VALIDATOR_MNEMONIC="$(python3 - "$ENV_FILE" <<'PY'
import sys, shlex
for line in open(sys.argv[1]):
    line = line.strip()
    if line.startswith('export '):
        line = line[len('export '):].lstrip()
    if line.startswith('VALIDATOR_MNEMONIC='):
        v = line[len('VALIDATOR_MNEMONIC='):]
        quoted = v[:1] in (chr(34), chr(39))
        print(' '.join(shlex.split(v)) if quoted else v.split('#')[0].strip())
        break
PY
)"
  [ -n "$VALIDATOR_MNEMONIC" ] || die "$ENV_FILE has no VALIDATOR_MNEMONIC"
fi

# ── scratch space; every source is restored unless the ceremony completes ───
WORK="$(mktemp -d)"
chmod 700 "$WORK"
cp "$SRC/accounts.json" "$SRC/chain.json" "$GENTX" "$REPO/networks/genesis.json" "$REPO/networks/genesis.json.sha256" "$WORK/"
DONE=0
cleanup() {
  if [ "$DONE" -ne 1 ]; then
    cp "$WORK/accounts.json" "$SRC/accounts.json"
    cp "$WORK/chain.json" "$SRC/chain.json"
    cp "$WORK/genesis-validator.json" "$GENTX"
    cp "$WORK/genesis.json" "$REPO/networks/genesis.json"
    cp "$WORK/genesis.json.sha256" "$REPO/networks/genesis.json.sha256"
    echo "ceremony: FAILED; every source restored" >&2
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

say() { printf '  %s\n' "$*" >&2; }

CHAIN_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["chain_id"])' "$SRC/chain.json")"
say "building earthd"
go build -o "$WORK/earthd" "$REPO/cmd/earthd"
H="$WORK/home"
# init writes a consensus and a node key into the scratch home; neither is
# used (--pubkey, --node-id) and both go with it.
"$WORK/earthd" init ceremony --chain-id "$CHAIN_ID" --home "$H" >/dev/null 2>&1

# ── 1. the operator key ─────────────────────────────────────────────────────
printf '%s\n' "$VALIDATOR_MNEMONIC" | "$WORK/earthd" keys add operator --recover --keyring-backend test --home "$H" >/dev/null 2>&1 \
  || die "the mnemonic does not import"
unset VALIDATOR_MNEMONIC
ADDR="$("$WORK/earthd" keys show operator -a --keyring-backend test --home "$H")"
[ "$ADDR" = "$OPERATOR" ] || die "the mnemonic is $ADDR, not the launch operator $OPERATOR"
say "operator $ADDR"

# ── 2. accounts.json ────────────────────────────────────────────────────────
python3 - "$SRC/accounts.json" "$OPERATOR" "$PLACEHOLDER" "$DEVNET_ACCOUNTS" <<'PY'
import json, sys, collections
path, op, placeholder, devnet = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].split()
d = json.load(open(path), object_pairs_hook=collections.OrderedDict)
keyed = d['keyed']
addrs = [a['address'] for a in keyed]
done = op in addrs and placeholder not in addrs and not set(devnet) & set(addrs)
pending = placeholder in addrs and op not in addrs and set(devnet) <= set(addrs)
if not (pending or done):
    sys.exit('ceremony: accounts.json is neither the placeholder set nor the launch set: %s' % addrs)
if pending:
    out = []
    for a in keyed:
        if a['address'] in devnet:
            continue
        if a['address'] == placeholder:
            a = collections.OrderedDict([
                ('address', op),
                ('coins', a['coins']),
                ('note', 'The genesis validator\'s operator account. 1,000 ERTH: enough to '
                         'self-delegate 100, and to fund governance deposits (5 ERTH expedited, 1 '
                         'normal) for years. Deliberately small, because the validator\'s real '
                         'income is the staking pillar, and a large genesis balance on top of that '
                         'is an allocation nobody voted for. Its key is VALIDATOR_MNEMONIC in the '
                         'gitignored .env of the deploy repo and is not in this repository; '
                         'scripts/ceremony.sh signs the gentx with it.'),
            ])
        out.append(a)
    d['keyed'] = out
open(path, 'w').write(json.dumps(d, indent=2, ensure_ascii=False) + '\n')
PY
say "accounts.json: launch set"

# ── 3. chain.json (one line, so the file keeps its layout) ──────────────────
python3 - "$SRC/chain.json" "$GENESIS_TIME" <<'PY'
import re, sys
path, t = sys.argv[1], sys.argv[2]
s = open(path).read()
s, n = re.subn(r'("genesis_time":\s*)"[^"]*"', lambda m: m.group(1) + '"%s"' % t, s)
if n != 1:
    sys.exit('ceremony: chain.json has %d genesis_time fields' % n)
open(path, 'w').write(s)
PY
say "chain.json: genesis_time $GENESIS_TIME"

# ── 4. the gentx, against the genesis it will be collected into ─────────────
FIELDS=$(python3 -c '
import json, sys
t = json.load(open(sys.argv[1]))
m = t["body"]["messages"][0]
v, c = m["value"], m["commission"]
print(m["description"]["moniker"], v["amount"] + v["denom"], c["rate"], c["max_rate"], c["max_change_rate"], m["min_self_delegation"], t["body"]["memo"], sep="\t")
' "$WORK/genesis-validator.json")
IFS=$'\t' read -r OLDMONIKER AMOUNT RATE MAXRATE MAXCHANGE MINSELF OLDMEMO <<<"$FIELDS"
MONIKER="${MONIKER:-$OLDMONIKER}"
MEMO_PEER="${MEMO_PEER:-$OLDMEMO}"
NODE_ID="${MEMO_PEER%%@*}"; HOSTPORT="${MEMO_PEER#*@}"
P2P_HOST="${HOSTPORT%:*}"; P2P_PORT="${HOSTPORT##*:}"
[ -n "$NODE_ID" ] && [ -n "$P2P_HOST" ] && [ -n "$P2P_PORT" ] || die "--memo-peer must be ID@HOST:PORT"

rm -f "$GENTX"   # signed by the placeholder account, which no longer exists
"$REPO/scripts/build-genesis.sh" -o "$WORK/pre-genesis.json" 2>"$WORK/build.log" || { cat "$WORK/build.log" >&2; die "pre-gentx genesis build failed"; }
cp "$WORK/pre-genesis.json" "$H/config/genesis.json"
"$WORK/earthd" genesis gentx operator "$AMOUNT" --chain-id "$CHAIN_ID" --moniker "$MONIKER" \
  --commission-rate "$RATE" --commission-max-rate "$MAXRATE" --commission-max-change-rate "$MAXCHANGE" \
  --min-self-delegation "$MINSELF" --pubkey "$PUBKEY" --node-id "$NODE_ID" --ip "$P2P_HOST" --p2p-port "$P2P_PORT" \
  --keyring-backend test --home "$H" --output-document "$WORK/gentx.json" >/dev/null 2>"$WORK/gentx.log" \
  || { cat "$WORK/gentx.log" >&2; die "gentx failed"; }
python3 - "$WORK/gentx.json" "$PUBKEY" <<'PY'
import json, sys
t, want = json.load(open(sys.argv[1])), json.loads(sys.argv[2])
m = t['body']['messages'][0]
if m['pubkey'] != want:
    sys.exit('ceremony: the gentx carries %s, not %s' % (m['pubkey'], want))
PY
cp "$WORK/gentx.json" "$GENTX"
say "gentx: $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["body"]["messages"][0]["validator_address"])' "$GENTX")"

# ── 5. the launch genesis, checked ──────────────────────────────────────────
"$REPO/scripts/build-genesis.sh"
"$REPO/scripts/build-genesis.sh" --check
say "genesis tests (ceremony required)"
(cd "$REPO" && EARTH_REQUIRE_CEREMONY=1 go test -count=1 ./networks/ >"$WORK/test.log" 2>&1) || { cat "$WORK/test.log" >&2; die "genesis tests failed"; }

DONE=1
SUM="$(cut -d' ' -f1 "$REPO/networks/genesis.json.sha256")"
echo
echo "launch genesis: networks/genesis.json  sha256 $SUM"
echo "  operator $OPERATOR, genesis_time $GENESIS_TIME"
echo "next: review git diff; commit networks/genesis/ with networks/genesis.json(.sha256); publish the sha256"
