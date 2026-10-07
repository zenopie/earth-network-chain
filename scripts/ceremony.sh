#!/usr/bin/env bash
#
# The launch ceremony: turn the placeholder genesis sources into the launch
# genesis, in one command, on the operator's machine.
#
#   scripts/ceremony.sh --launch <launch.json> --genesis-time <RFC3339> \
#       --memo-peer ID@HOST:PORT --moniker NAME [--env-file PATH] \
#       [--allow-empty-remove]
#
#   --launch FILE    the launch identities, which this repository does not
#                    hold (the operator keeps them with its deployment):
#                      {
#                        "operator": "earth1…",         the genesis validator's
#                                                       operator account
#                        "consensus_pubkey": "<base64>", its ed25519 consensus key
#                                                       (`earthd comet show-validator`'s
#                                                       "key"); only the public key,
#                                                       the private key never touches
#                                                       this machine
#                        "remove_accounts": ["earth1…"], placeholder accounts that
#                                                       must not reach the launch
#                                                       genesis; empty only with
#                                                       --allow-empty-remove
#                        "used_consensus_keys": ["…"]   consensus keys that signed an
#                                                       earlier chain under this id:
#                                                       refused (may be empty)
#                      }
#                    Required, all four keys. Keys are canonical base64 of 32
#                    bytes and are compared as bytes. Whatever the list says,
#                    the launch accounts.json may hold no keyed account but
#                    the operator: one left over is refused (as the genesis
#                    tests do).
#   --allow-empty-remove
#                    accept an empty remove_accounts (only meaningful when the
#                    placeholder set holds no account besides the placeholder
#                    validator; anything else is still refused).
#   --genesis-time   the launch instant, e.g. 2026-10-20T16:00:00Z (UTC, whole
#                    seconds, in the future). Written to networks/genesis/chain.json.
#   --env-file PATH  a dotenv file holding VALIDATOR_MNEMONIC (only that line is
#                    read). Required unless VALIDATOR_MNEMONIC is already set in
#                    the environment.
#   --memo-peer ID@HOST:PORT
#                    the gentx memo: the validator's public p2p address, the
#                    genesis's only advertised peer. Required. HOST is a
#                    public IP (is_global: private, CGNAT 100.64.0.0/10,
#                    loopback, link-local, reserved and documentation ranges
#                    are refused) or a fully qualified DNS name that should
#                    resolve to public addresses only; it is resolved here
#                    warned (not refused) if it does not resolve; refused if any address is
#                    not public. Use a name only if it resolves the same,
#                    publicly, from everywhere (no split-horizon or
#                    internal zone); prefer the public IP.
#   --moniker NAME   the validator's moniker. Required; the placeholder gentx's
#                    moniker is refused.
#
# What it does, all or nothing (any failure restores every source it touched):
#
#   1. reads VALIDATOR_MNEMONIC (only that line of the env file; never printed)
#      into a throwaway test keyring and checks it is the launch operator;
#   2. networks/genesis/accounts.json: removes remove_accounts, and swaps the
#      placeholder validator account (the committed gentx's signer) for the
#      operator with the same balance;
#   3. networks/genesis/chain.json: genesis_time;
#   4. networks/genesis/gentx/genesis-validator.json: a new gentx signed by the
#      operator with the given consensus key, memo and moniker (self-delegation
#      and commission kept from the placeholder);
#   5. make genesis, then make genesis-check and the genesis tests with the
#      ceremony required (EARTH_REQUIRE_CEREMONY=1 EARTH_CEREMONY_CONFIG=<launch.json>
#      go test ./networks/).
#
# Afterwards: review `git diff`, commit the sources with networks/genesis.json
# and its .sha256, and publish the printed sha256. Re-running it on finished
# sources (same operator) is allowed: it re-signs and rebuilds.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$REPO/networks/genesis"
GENTX="$SRC/gentx/genesis-validator.json"

LAUNCH="" GENESIS_TIME="" ENV_FILE="" MEMO_PEER="" MONIKER="" ALLOW_EMPTY_REMOVE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --launch) LAUNCH="$2"; shift 2 ;;
    --genesis-time) GENESIS_TIME="$2"; shift 2 ;;
    --env-file) ENV_FILE="$2"; shift 2 ;;
    --memo-peer) MEMO_PEER="$2"; shift 2 ;;
    --moniker) MONIKER="$2"; shift 2 ;;
    --allow-empty-remove) ALLOW_EMPTY_REMOVE=1; shift ;;
    -h|--help) sed -n '2,80p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
die() { echo "ceremony: $*" >&2; exit 1; }
[ -n "$LAUNCH" ] || die "--launch <launch.json> (the launch identities) is required"
[ -f "$LAUNCH" ] || die "--launch $LAUNCH: no such file"
LAUNCH="$(cd "$(dirname "$LAUNCH")" && pwd)/$(basename "$LAUNCH")"
[ -n "$GENESIS_TIME" ] || die "--genesis-time <RFC3339> is required"
# The memo and moniker are sha-pinned into the launch genesis: no defaults
# from the placeholder (a LAN peer and a devnet name, audit D-8).
[ -n "$MEMO_PEER" ] || die "--memo-peer ID@HOST:PORT (the validator's public p2p address) is required"
[ -n "$MONIKER" ] || die "--moniker NAME is required"

# ── the launch identities ───────────────────────────────────────────────────
# One line each: OPERATOR, PUBKEY (the gentx --pubkey JSON), REMOVE (space
# separated), USED (space separated).
LAUNCH_VALUES="$(python3 - "$LAUNCH" "$ALLOW_EMPTY_REMOVE" <<'PY'
import sys, json, base64, re, binascii
try:
    d = json.load(open(sys.argv[1]))
except ValueError as e:
    sys.exit('ceremony: --launch is not JSON: %s' % e)
need = {'operator', 'consensus_pubkey', 'remove_accounts', 'used_consensus_keys'}
if not isinstance(d, dict) or not need <= set(d):
    sys.exit('ceremony: --launch must hold %s' % ', '.join(sorted(need)))
addr = re.compile(r'earth1[02-9ac-hj-np-z]{38}')
op = d['operator']
if not isinstance(op, str) or not addr.fullmatch(op):
    sys.exit('ceremony: launch operator %r is not an earth1 account address' % op)
def ed25519(what, k):
    # Canonical standard base64 of exactly 32 bytes: decoding alone accepts
    # non-zero pad bits, so two spellings of one key would compare unequal.
    if not isinstance(k, str):
        sys.exit('ceremony: %s is not a base64 string' % what)
    try:
        raw = base64.b64decode(k, validate=True)
    except (binascii.Error, ValueError):
        sys.exit('ceremony: %s is not base64: %r' % (what, k))
    if base64.b64encode(raw).decode() != k:
        sys.exit('ceremony: %s is not canonical base64: %r' % (what, k))
    if len(raw) != 32:
        sys.exit('ceremony: %s is %d bytes, not 32 (ed25519)' % (what, len(raw)))
    return raw
key = d['consensus_pubkey']
raw = ed25519('launch consensus_pubkey', key)
rm, used = d['remove_accounts'], d['used_consensus_keys']
if not isinstance(rm, list) or not all(isinstance(a, str) and addr.fullmatch(a) for a in rm):
    sys.exit('ceremony: launch remove_accounts must be a list of earth1 addresses')
if op in rm:
    sys.exit('ceremony: the launch operator is in remove_accounts')
if not rm and sys.argv[2] != '1':
    sys.exit('ceremony: launch remove_accounts is empty: the placeholder accounts would reach the launch '
             'genesis (pass --allow-empty-remove if the placeholder set really has none)')
if not isinstance(used, list):
    sys.exit('ceremony: launch used_consensus_keys must be a list of base64 keys')
used_raw = [ed25519('used_consensus_keys[%d]' % i, k) for i, k in enumerate(used)]
if raw in used_raw:
    sys.exit('ceremony: consensus key %s signed an earlier chain: refusing to reuse it' % key)
print(op)
print(json.dumps({'@type': '/cosmos.crypto.ed25519.PubKey', 'key': key}, separators=(',', ':')))
print(' '.join(rm))
print(' '.join(used))
print('end')
PY
)" || exit 1
{ read -r OPERATOR; read -r PUBKEY; read -r REMOVE_ACCOUNTS; read -r USED_CONSENSUS_KEYS; read -r _END; } <<<"$LAUNCH_VALUES"

# The placeholder: whoever signed the committed gentx, and its moniker. A
# re-run on finished sources finds the operator here instead.
GENTX_FIELDS="$(python3 - "$GENTX" <<'PY'
import json, sys
# bech32 (BIP 173): the operator account is the bytes of the valoper address
# under the account prefix.
CS = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l'
def polymod(v):
    g = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3]
    c = 1
    for x in v:
        b = c >> 25
        c = (c & 0x1ffffff) << 5 ^ x
        for i in range(5):
            c ^= g[i] if (b >> i) & 1 else 0
    return c
def hrpx(h): return [ord(x) >> 5 for x in h] + [0] + [ord(x) & 31 for x in h]
def decode(a):
    h, d = a[:a.rindex('1')], [CS.index(x) for x in a[a.rindex('1') + 1:]]
    if polymod(hrpx(h) + d) != 1:
        sys.exit('ceremony: bad bech32 %s' % a)
    return d[:-6]
def encode(h, d):
    p = polymod(hrpx(h) + d + [0] * 6) ^ 1
    return h + '1' + ''.join(CS[x] for x in d + [(p >> 5 * (5 - i)) & 31 for i in range(6)])
t = json.load(open(sys.argv[1]))
m = t['body']['messages'][0]
print(encode('earth', decode(m['validator_address'])))
print(m['description']['moniker'])
PY
)" || exit 1
GENTX_SIGNER="${GENTX_FIELDS%%$'\n'*}"; PLACEHOLDER_MONIKER="${GENTX_FIELDS#*$'\n'}"
[ "$MONIKER" != "$PLACEHOLDER_MONIKER" ] || [ "$GENTX_SIGNER" = "$OPERATOR" ] \
  || die "--moniker $MONIKER is the placeholder gentx's moniker"
python3 - "$MEMO_PEER" <<'PY' || exit 1
import sys, ipaddress, re, socket
peer = sys.argv[1]
m = re.fullmatch(r'([0-9a-f]{40})@(.+):([0-9]{1,5})', peer)
if not m:
    sys.exit('ceremony: --memo-peer must be <40-hex node id>@HOST:PORT, got %r' % peer)
host, port = m.group(2), int(m.group(3))
if not 0 < port < 65536:
    sys.exit('ceremony: --memo-peer port %d is out of range' % port)

def public(ip):
    # is_global leaves out private, loopback, link-local, unspecified,
    # reserved, documentation and shared (CGNAT 100.64.0.0/10) ranges.
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
        ip = ip.ipv4_mapped
    return ip.is_global and not ip.is_multicast

bare = host[1:-1] if host.startswith('[') and host.endswith(']') else host
try:
    ip = ipaddress.ip_address(bare)
except ValueError:
    ip = None
if ip is not None:
    if not public(ip):
        sys.exit('ceremony: --memo-peer host %s is not a public address (private, CGNAT, loopback, '
                 'link-local, reserved or multicast)' % host)
else:
    # A DNS name must be a fully qualified public name that resolves, from
    # here, to public addresses only. Every node in the world dials it from
    # the genesis, so a name that resolves privately anywhere (split-horizon,
    # an internal zone) is the operator's to rule out; this refuses what can
    # be seen from this machine.
    name = host.rstrip('.').lower()
    labels = name.split('.')
    if (len(labels) < 2 or not all(re.fullmatch(r'[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?', l) for l in labels)
            or labels[-1] in ('localhost', 'local', 'internal', 'lan', 'home', 'corp', 'intranet', 'test', 'invalid', 'example')
            or name.endswith('.home.arpa')):
        sys.exit('ceremony: --memo-peer host %s is not a public DNS name' % host)
    try:
        addrs = {ipaddress.ip_address(a[4][0].split('%')[0]) for a in socket.getaddrinfo(name, port, proto=socket.IPPROTO_TCP)}
    except (socket.gaierror, UnicodeError) as e:
        # Nothing dials the memo (peers come from config and the docs), and a
        # fresh record may not have reached this machine's resolver yet: a
        # well-formed public name is enough. A name that does resolve must
        # resolve to public addresses only.
        print('ceremony: warning: --memo-peer host %s does not resolve here (%s); using it anyway' % (host, e), file=sys.stderr)
        sys.exit(0)
    bad = sorted(str(a) for a in addrs if not public(a))
    if not addrs or bad:
        sys.exit('ceremony: --memo-peer host %s resolves to non-public %s' % (host, ', '.join(bad) or 'nothing'))
PY

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

# ── the mnemonic: from the environment, or that one line of --env-file ──────
if [ -z "${VALIDATOR_MNEMONIC:-}" ]; then
  [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ] || die "no VALIDATOR_MNEMONIC in the environment and no --env-file PATH holding it"
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
go -C "$REPO" build -o "$WORK/earthd" ./cmd/earthd
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
python3 - "$SRC/accounts.json" "$OPERATOR" "$GENTX_SIGNER" "$REMOVE_ACCOUNTS" <<'PY'
import json, sys, collections
# placeholder is the committed gentx's signer: the operator itself on a re-run.
path, op, placeholder, remove = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].split()
d = json.load(open(path), object_pairs_hook=collections.OrderedDict)
keyed = d['keyed']
addrs = [a['address'] for a in keyed]
done = placeholder == op and op in addrs and not set(remove) & set(addrs)
pending = placeholder != op and placeholder in addrs and op not in addrs and set(remove) <= set(addrs)
if not (pending or done):
    sys.exit('ceremony: accounts.json is neither the placeholder set nor the launch set: %s' % addrs)
if pending:
    out = []
    for a in keyed:
        if a['address'] in remove:
            continue
        if a['address'] == placeholder:
            a = collections.OrderedDict([
                ('address', op),
                ('coins', a['coins']),
                ('note', 'The genesis validator\'s operator account, with the placeholder\'s '
                         'balance: enough to self-delegate and to fund governance deposits for years. '
                         'Deliberately small, because the validator\'s real income is the staking '
                         'pillar, and a large genesis balance on top of that is an allocation nobody '
                         'voted for. Its key is held by the operator, not in this repository; '
                         'scripts/ceremony.sh signs the gentx with it.'),
            ])
        out.append(a)
    d['keyed'] = out
# The launch set is the operator alone: a placeholder account the launch file
# forgot to remove (a devnet faucet, a hot wallet whose key has been on a
# laptop) is refused here, as networks/ceremony_test.go refuses it.
left = [a['address'] for a in d['keyed'] if a['address'] != op]
if left:
    sys.exit('ceremony: accounts.json would keep keyed accounts besides the operator: %s '
             '(add them to the launch file\'s remove_accounts)' % ', '.join(left))
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
IFS=$'\t' read -r _ AMOUNT RATE MAXRATE MAXCHANGE MINSELF _ <<<"$FIELDS"
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
(cd "$REPO" && EARTH_REQUIRE_CEREMONY=1 EARTH_CEREMONY_CONFIG="$LAUNCH" go test -count=1 ./networks/ >"$WORK/test.log" 2>&1) || { cat "$WORK/test.log" >&2; die "genesis tests failed"; }

DONE=1
SUM="$(cut -d' ' -f1 "$REPO/networks/genesis.json.sha256")"
echo
echo "launch genesis: networks/genesis.json  sha256 $SUM"
echo "  operator $OPERATOR, genesis_time $GENESIS_TIME"
echo "next: review git diff; commit networks/genesis/ with networks/genesis.json(.sha256); publish the sha256"
