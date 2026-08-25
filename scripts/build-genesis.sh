#!/usr/bin/env bash
#
# Build networks/genesis.json from the sources in networks/genesis/.
#
# The genesis file is a build artifact, not something anyone edits. Every node on
# the network has to agree on it byte for byte, so the only safe way to produce
# it is a command that runs the same way twice. It used to be `ignite chain init`
# followed by hand-stripping gentxs and dev accounts and recomputing bank supply
# by eye — three chances to ship a file whose supply does not equal its balances,
# with a consensus failure at the end of it.
#
# What goes in:
#
#   networks/genesis/chain.json        chain id, genesis time, app version, block gas limit
#   networks/genesis/app_state.json    the parameters this chain deliberately sets
#   networks/genesis/accounts.json     every balance that exists at height 1
#   networks/genesis/verifying-keys/   one base64 UltraHonk key per register circuit
#   networks/genesis/gentx/            signed gentxs to collect, if any
#   csca/                            the CSCA trust store, via tools/pki-genesis
#
# Everything else is whatever `earthd init` produces for the SDK version this
# repo builds against. app_state.json is merged *over* that, so a field the SDK
# adds arrives with its upstream default instead of silently going missing.
#
#   scripts/build-genesis.sh              write networks/genesis.json
#   scripts/build-genesis.sh --check      rebuild and diff; fail if it drifted
#   scripts/build-genesis.sh -o out.json  write somewhere else
#
# Verify with: go test ./deploy/...
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$REPO/networks/genesis"
OUT="$REPO/networks/genesis.json"
CHECK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --check) CHECK=1; shift ;;
    -o|--out) OUT="$2"; shift 2 ;;
    -h|--help) sed -n '2,32p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

say() { printf '  %s\n' "$*" >&2; }

CHAIN_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["chain_id"])' "$SRC/chain.json")"

# ── 1. a stock node, for the SDK's own defaults ─────────────────────────────
#
# Built from this tree rather than from $PATH: the genesis must match the binary
# it ships with, and a stale earthd on the operator's machine is exactly the kind
# of difference that only shows up as a mismatched app hash at height 1.
say "building earthd"
go build -o "$WORK/earthd" "$REPO/cmd/earthd"

say "earthd init ($CHAIN_ID)"
"$WORK/earthd" init genesis-build --chain-id "$CHAIN_ID" --home "$WORK/home" >/dev/null 2>&1
GEN="$WORK/home/config/genesis.json"

# ── 2. the CSCA trust store ─────────────────────────────────────────────────
#
# Regenerated from the certificates rather than carried along, so the trust store
# on disk and the one in genesis cannot disagree about which passports the chain
# will accept.
say "generating pki.cscas from csca/"
# additional/ is allowed to be empty — the ICAO master list is the whole trust
# store unless a CSCA has been added by hand. Without nullglob an empty
# directory passes the literal `*.cer` through as a filename and the build dies
# on a file that does not exist; the `[@]+` form is because `set -u` treats an
# empty array as unbound on bash 3.2, which is what macOS ships.
shopt -s nullglob
extra_cscas=("$REPO"/csca/additional/*.cer)
shopt -u nullglob
go run "$REPO/tools/pki-genesis" \
  "$REPO/csca/masterlist/allowlist.ml" ${extra_cscas[@]+"${extra_cscas[@]}"} \
  > "$WORK/cscas.json" 2>"$WORK/pki.log" || { cat "$WORK/pki.log" >&2; exit 1; }
say "$(tail -n1 "$WORK/pki.log")"

# ── 3. merge the overrides, the CSCAs and the verifying keys ────────────────
say "merging app_state overrides"
python3 - "$GEN" "$SRC" "$WORK/cscas.json" <<'PY'
import json, sys, collections, glob, os
od = collections.OrderedDict
gen_path, src, cscas_path = sys.argv[1], sys.argv[2], sys.argv[3]

g = json.load(open(gen_path), object_pairs_hook=od)

def merge(base, over):
    """Deep-merge over into base. Lists replace wholesale — a genesis list is a
    complete statement (every pool, every CSCA), never an addition to whatever
    the SDK happened to ship."""
    for k, v in over.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            merge(base[k], v)
        else:
            base[k] = v

merge(g['app_state'], json.load(open(os.path.join(src, 'app_state.json')), object_pairs_hook=od))

g['app_state']['pki']['cscas'] = json.load(open(cscas_path), object_pairs_hook=od)

vks = od()
for p in sorted(glob.glob(os.path.join(src, 'verifying-keys', '*.vk.b64'))):
    cid = os.path.basename(p)[:-len('.vk.b64')]
    vks[cid] = open(p).read().strip()
if not vks:
    sys.exit('no verifying keys in networks/genesis/verifying-keys — MsgRegister would '
             'always fail and the chain could never register a human')
g['app_state']['personhood']['params']['verifying_keys'] = vks

json.dump(g, open(gen_path, 'w'), indent=2)
print('  %d CSCAs, %d verifying keys: %s' % (
    len(g['app_state']['pki']['cscas']), len(vks), ', '.join(vks)), file=sys.stderr)
PY

# ── 4. accounts ─────────────────────────────────────────────────────────────
#
# Keyed accounts go through add-genesis-account, which moves auth, balances and
# supply together. Module accounts cannot: the tool writes a BaseAccount, and
# x/auth has to be free to put a ModuleAccount at that address. Their balances
# are written straight to bank and the supply is recomputed from the total.
say "adding accounts"
while IFS=$'\t' read -r addr coins; do
  [ -n "$addr" ] || continue
  say "  keyed  $addr  $coins"
  "$WORK/earthd" genesis add-genesis-account "$addr" "$coins" --home "$WORK/home"
done < <(python3 -c '
import json,sys
for a in json.load(open(sys.argv[1]))["keyed"]:
    print(a["address"], a["coins"], sep="\t")' "$SRC/accounts.json")

python3 - "$GEN" "$SRC/accounts.json" <<'PY'
import json, sys, collections
od = collections.OrderedDict
gen_path, acct_path = sys.argv[1], sys.argv[2]
g = json.load(open(gen_path), object_pairs_hook=od)
bank = g['app_state']['bank']

for m in json.load(open(acct_path))['modules']:
    coins = sorted(m['coins'], key=lambda c: c['denom'])  # bank requires sorted denoms
    bank['balances'].append(od([('address', m['address']),
                                ('coins', [od([('denom', c['denom']), ('amount', c['amount'])])
                                           for c in coins])]))
    print('  module %s  %s' % (m['name'], ', '.join(c['amount'] + c['denom'] for c in coins)),
          file=sys.stderr)

bank['balances'].sort(key=lambda b: b['address'])

# Supply is derived, never asserted. Anything else is a way to ship a file whose
# supply and balances disagree, which x/bank rejects at InitGenesis — if you are
# lucky, and which drifts silently if you are not.
total = collections.Counter()
for b in bank['balances']:
    for c in b['coins']:
        total[c['denom']] += int(c['amount'])
bank['supply'] = [od([('denom', d), ('amount', str(total[d]))]) for d in sorted(total)]

json.dump(g, open(gen_path, 'w'), indent=2)
PY

# ── 5. gentxs ───────────────────────────────────────────────────────────────
#
# Zero is a valid answer: the chain can launch with an empty validator set and
# take MsgCreateValidator after height 1. What is not valid is generating one per
# node at boot, which is what the container does today — every node then computes
# a different genesis and none of them can talk to each other.
if compgen -G "$SRC/gentx/*.json" >/dev/null 2>&1; then
  say "collecting gentxs"
  mkdir -p "$WORK/home/config/gentx"
  cp "$SRC"/gentx/*.json "$WORK/home/config/gentx/"
  "$WORK/earthd" genesis collect-gentxs --home "$WORK/home" >/dev/null 2>&1
  say "  $(ls -1 "$SRC"/gentx/*.json | wc -l | tr -d ' ') gentx(s)"
else
  say "no gentxs in networks/genesis/gentx — launching with an empty validator set"
fi

# ── 6. the header ───────────────────────────────────────────────────────────
say "stamping chain.json"
python3 - "$GEN" "$SRC/chain.json" <<'PY'
import json, sys, collections
od = collections.OrderedDict
gen_path, chain_path = sys.argv[1], sys.argv[2]
g = json.load(open(gen_path), object_pairs_hook=od)
c = json.load(open(chain_path))
g['genesis_time'] = c['genesis_time']
g['chain_id'] = c['chain_id']
g['initial_height'] = c['initial_height']
g['consensus']['params']['version']['app'] = c['app_version']
g['consensus']['params']['block']['max_gas'] = c['block_max_gas']
# `earthd init` fills these from the build's ldflags, which carry the git commit.
# Pinned instead, or the artifact would differ for every person who built it.
g['app_name'] = c['app_name']
g['app_version'] = c['binary_version']
json.dump(g, open(gen_path, 'w'), indent=2)
PY

# ── 7. validate, then publish ───────────────────────────────────────────────
say "validate-genesis"
"$WORK/earthd" genesis validate-genesis --home "$WORK/home" >/dev/null

# Canonical form: keys sorted, two-space indent, trailing newline. The point is
# not tidiness — it is that two people who run this script get the same bytes and
# therefore the same sha256, even if a future SDK emits its defaults in a
# different order. The published hash is only worth anything if it is stable.
python3 -c '
import json, sys
p = sys.argv[1]
g = json.load(open(p))
json.dump(g, open(p, "w"), indent=2, sort_keys=True)
open(p, "a").write("\n")' "$GEN"

SUM="$(shasum -a 256 "$GEN" | cut -d' ' -f1)"

if [ "$CHECK" -eq 1 ]; then
  if diff -q "$GEN" "$OUT" >/dev/null 2>&1; then
    say "up to date  sha256 $SUM"
    exit 0
  fi
  echo "genesis is stale — networks/genesis.json does not match its sources." >&2
  echo "Run scripts/build-genesis.sh and commit the result." >&2
  diff -u "$OUT" "$GEN" | head -60 >&2
  exit 1
fi

cp "$GEN" "$OUT"
printf '%s  %s\n' "$SUM" "$(basename "$OUT")" > "$OUT.sha256"
say "wrote $OUT"
say "sha256 $SUM"
