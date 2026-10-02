#!/usr/bin/env bash
#
# The privacy circuits' verifying keys, regenerated from the circuits.
#
# x/shielded verifies every private tx against two UltraHonk keys: action
# (every action of every shielded bundle: spends, outputs, fees) and
# membership (personhood's claims, votes, caretaker splits, referrer
# bindings). With either missing from
# genesis every private msg is refused and the chain launches with private
# txs disabled. This script compiles both circuits from the mobile repo and
# writes each key everywhere the chain reads it:
#
#   networks/genesis/shielded-verifying-keys/<circuit>.vk.b64   launch genesis source
#   config.yml  genesis.app_state.shielded.params.verifying_keys  dev chain
#   x/shielded/testdata/action.vk                                 tests (raw)
#   x/personhood/testdata/app/membership.vk                       tests (raw)
#
#   ./scripts/privacy-vks.sh [--check] [path-to-earth-network-mobile/circuits]
#
# --check writes nothing: it fails unless every copy equals what the circuits
# produce now (make privacy-vks-check). After a circuit change, run without it,
# then regenerate every proof fixture (scripts/*-fixtures.sh,
# scripts/orchard-bundles.sh) and make genesis.
# Requires nargo and bb (v5.0.0) on PATH or in ~/.nargo/bin, ~/.bb.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK=0
if [ "${1:-}" = "--check" ]; then CHECK=1; shift; fi
CIRCUITS_SRC="${1:-$CHAIN_DIR/../earth-network-mobile/circuits}"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"
for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
for c in action membership; do
  [ -d "$CIRCUITS_SRC/$c" ] || { echo "error: no $c circuit under $CIRCUITS_SRC" >&2; exit 1; }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -R "$CIRCUITS_SRC" "$WORK/circuits"
rm -rf "$WORK/circuits/target"
for c in action membership; do
  ( cd "$WORK/circuits" && nargo compile --package "$c" >/dev/null \
      && bb write_vk -b "target/$c.json" -o "$WORK/vk-$c" -t noir-recursive >/dev/null 2>&1 )
  base64 < "$WORK/vk-$c/vk" | tr -d '\n' > "$WORK/$c.vk.b64"
done

GEN="$CHAIN_DIR/networks/genesis/shielded-verifying-keys"
raw_targets() {
  echo "action $CHAIN_DIR/x/shielded/testdata/action.vk"
  echo "membership $CHAIN_DIR/x/personhood/testdata/app/membership.vk"
}

if [ "$CHECK" -eq 1 ]; then
  fail=0
  for c in action membership; do
    cmp -s "$WORK/$c.vk.b64" "$GEN/$c.vk.b64" || { echo "stale: $GEN/$c.vk.b64" >&2; fail=1; }
  done
  while read -r c f; do
    cmp -s "$WORK/vk-$c/vk" "$f" || { echo "stale: $f" >&2; fail=1; }
  done < <(raw_targets)
  python3 - "$CHAIN_DIR/config.yml" "$WORK" <<'PY' || fail=1
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
vks = cfg['genesis']['app_state']['shielded']['params']['verifying_keys']
bad = [c for c in ('action', 'membership') if vks.get(c) != open(f'{sys.argv[2]}/{c}.vk.b64').read()]
if bad:
    sys.exit('stale: config.yml shielded verifying_keys: ' + ', '.join(bad))
PY
  [ "$fail" -eq 0 ] || { echo "verifying keys do not match the circuits: run scripts/privacy-vks.sh" >&2; exit 1; }
  echo "verifying keys match the circuits"
  exit 0
fi

mkdir -p "$GEN"
rm -f "$GEN/transfer.vk.b64" # the retired 3-in/3-out transfer circuit
for c in action membership; do cp "$WORK/$c.vk.b64" "$GEN/$c.vk.b64"; done
while read -r c f; do cp "$WORK/vk-$c/vk" "$f"; done < <(raw_targets)
python3 - "$CHAIN_DIR/config.yml" "$WORK" <<'PY'
import re, sys
path, work = sys.argv[1], sys.argv[2]
s = open(path).read()
keys = {c: open(f'{work}/{c}.vk.b64').read() for c in ('action', 'membership')}
block = ('    # x/shielded verifies every private tx against these (bb v5.0.0\n'
         '    # UltraHonk, noir-recursive), written by scripts/privacy-vks.sh from the\n'
         '    # circuits. Without them every private msg is refused.\n'
         '    shielded:\n      params:\n        verifying_keys:\n' +
         ''.join(f'          {c}: "{v}"\n' for c, v in keys.items()))
pat = re.compile(r'    # x/shielded verifies every private tx.*?\n    shielded:\n      params:\n        verifying_keys:\n(?:          [^\n]*\n)+', re.S)
if pat.search(s):
    s = pat.sub(lambda _: block, s, count=1)
else:
    i = s.index('    personhood:\n')
    s = s[:i] + block + s[i:]
open(path, 'w').write(s)
PY
echo "wrote verifying keys; now: make genesis"
