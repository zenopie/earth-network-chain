#!/usr/bin/env bash
#
# The privacy circuits' and the passport register circuits' verifying keys,
# regenerated from the circuits.
#
# x/shielded verifies every private tx against five UltraHonk keys: action
# (every action of every shielded bundle: spends, outputs, fees), membership
# (personhood's claims, caretaker splits and handles, and the assembly's
# votes), stake (x/shieldedstaking's owner-locked stake notes) and vote (up
# to two stake notes' vote on a proposal, without spending them) and move (a
# handle or caretaker split passing to the same passport's next identity).
# With one missing from
# genesis the msgs needing it are refused. This script compiles the circuits
# from the mobile repo and writes each key everywhere the chain reads it:
#
#   networks/genesis/shielded-verifying-keys/<circuit>.vk.b64   launch genesis source
#   config.yml  genesis.app_state.shielded.params.verifying_keys  dev chain
#   x/shielded/testdata/action.vk                                 tests (raw)
#   x/personhood/testdata/app/membership.vk                       tests (raw)
#   x/shieldedstaking/testdata/stake.vk                           tests (raw)
#   x/shieldedstaking/testdata/vote.vk                            tests (raw)
#   x/personhood/testdata/app/move.vk                             tests (raw)
#
# and every passport register circuit in circuits/variants.json (one per DSC
# key type, signature scheme and hash profile; PASSPORT_COVERAGE.md):
#
#   networks/genesis/verifying-keys/<variant>.vk.b64              launch genesis source
#   config.yml  genesis.app_state.personhood.params.verifying_keys  dev chain
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
for c in action membership stake vote move; do
  [ -d "$CIRCUITS_SRC/$c" ] || { echo "error: no $c circuit under $CIRCUITS_SRC" >&2; exit 1; }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -R "$CIRCUITS_SRC" "$WORK/circuits"
rm -rf "$WORK/circuits/target"
( cd "$WORK/circuits" && nargo compile --workspace >/dev/null 2>"$WORK/compile.log" ) || { cat "$WORK/compile.log" >&2; exit 1; }
# Audit R2C-1: nargo reports every Brillig call it cannot see constrained.
# The known sites are false positives (scripts/brillig-allowlist.txt, each
# with its justification); any other site fails this script, in both modes,
# so a real unconstrained hint cannot hide among the known ones. Each site's
# warning count is pinned too, and a caller-dependent site's set of circuits
# (audit R3C-1), so a new caller of a listed library site fails rather than
# being inherited.
python3 - "$WORK/compile.log" "$CHAIN_DIR/scripts/brillig-allowlist.txt" <<'PY'
import re, sys
from collections import Counter
log = re.sub(r'\x1b\[[0-9;]*m', '', open(sys.argv[1], encoding='utf-8', errors='replace').read())
# site -> (pinned count, pinned circuit set or None for '*')
pinned = {}
for line in open(sys.argv[2], encoding='utf-8'):
    line = line.rstrip('\n')
    if not line or line.startswith('#'):
        continue
    cols = line.split('\t', 3)
    if len(cols) != 4 or not cols[3].strip():
        sys.exit(f'brillig-allowlist.txt: {cols[0]} needs site, count, circuits and a justification, TAB-separated')
    site, count, circuits, _ = cols
    if not count.isdigit() or int(count) < 1:
        sys.exit(f'brillig-allowlist.txt: {site} has a bad count {count!r}')
    if site in pinned:
        sys.exit(f'brillig-allowlist.txt: {site} is listed twice')
    pinned[site] = (int(count), None if circuits == '*' else set(circuits.split(',')))

def short(path):
    return path.split('/github.com/', 1)[1] if '/github.com/' in path else path

# Each warning: its site (first span) and the circuit it was compiled for
# (the package of the call stack's outermost frame, `1: main`).
seen = {}
blocks = re.split(r"^bug: Brillig function call isn't properly covered[^\n]*\n", log, flags=re.M)
if len(blocks) - 1 != log.count("Brillig function call isn't properly covered"):
    sys.exit('privacy-vks: unparsed Brillig warning (not at a line start)')
for n, b in enumerate(blocks[1:], 1):
    head = b.split('\nbug:', 1)[0].split('\nwarning:', 1)[0]
    m = re.search(r'^\s*┌─ (\S+):(\d+):\d+', head, flags=re.M)
    if not m:
        sys.exit(f'privacy-vks: Brillig warning #{n} has no parseable span')
    site = f'{short(m.group(1))}:{m.group(2)}'
    stack = head.split('= Call stack:', 1)
    frames = re.findall(r'^\s*\d+: \S+\s*\n\s*at (\S+):\d+:\d+', stack[1], flags=re.M) if len(stack) == 2 else []
    circuit = short(frames[0]).split('/', 1)[0] if frames else None
    seen.setdefault(site, []).append(circuit)

fail = False
for site, circuits in sorted(seen.items()):
    if site not in pinned:
        print(f'unreviewed Brillig warning: {site} ({len(circuits)}x)', file=sys.stderr)
        fail = True
for site, (count, want) in sorted(pinned.items()):
    got = seen.get(site, [])
    if len(got) != count:
        print(f'Brillig site {site}: {len(got)} warnings, allowlist pins {count}', file=sys.stderr)
        fail = True
    if want is not None:
        have = Counter(got)
        if None in have or set(have) != want or any(v != 1 for v in have.values()):
            print(f'Brillig site {site}: reached from {sorted(map(str, have.elements()))}, '
                  f'allowlist pins {sorted(want)}', file=sys.stderr)
            fail = True
if fail:
    sys.exit('a Brillig call nargo cannot see constrained is not as scripts/brillig-allowlist.txt pins it: '
             'review it (constrain the hint, or justify and update the site, its count and its circuits)')
print(f'Brillig warnings: {sum(len(v) for v in seen.values())} at {len(seen)} allowlisted sites, counts and callers as pinned', file=sys.stderr)
PY
for c in action membership stake vote move; do
  bb write_vk -b "$WORK/circuits/target/$c.json" -o "$WORK/vk-$c" -t noir-recursive >/dev/null 2>&1
  base64 < "$WORK/vk-$c/vk" | tr -d '\n' > "$WORK/$c.vk.b64"
done
VARIANTS=$(python3 -c 'import json,sys; print(" ".join(v["id"] for v in json.load(open(sys.argv[1]))["variants"]))' "$WORK/circuits/variants.json")
mkdir -p "$WORK/register"
for v in $VARIANTS; do
  bb write_vk -b "$WORK/circuits/target/$v.json" -o "$WORK/vk-$v" -t noir-recursive >/dev/null 2>&1
  base64 < "$WORK/vk-$v/vk" | tr -d '\n' > "$WORK/register/$v.vk.b64"
done
REG="$CHAIN_DIR/networks/genesis/verifying-keys"

GEN="$CHAIN_DIR/networks/genesis/shielded-verifying-keys"
raw_targets() {
  echo "action $CHAIN_DIR/x/shielded/testdata/action.vk"
  echo "membership $CHAIN_DIR/x/personhood/testdata/app/membership.vk"
  echo "stake $CHAIN_DIR/x/shieldedstaking/testdata/stake.vk"
  echo "vote $CHAIN_DIR/x/shieldedstaking/testdata/vote.vk"
  echo "move $CHAIN_DIR/x/personhood/testdata/app/move.vk"
}

if [ "$CHECK" -eq 1 ]; then
  fail=0
  for c in action membership stake vote move; do
    cmp -s "$WORK/$c.vk.b64" "$GEN/$c.vk.b64" || { echo "stale: $GEN/$c.vk.b64" >&2; fail=1; }
  done
  while read -r c f; do
    cmp -s "$WORK/vk-$c/vk" "$f" || { echo "stale: $f" >&2; fail=1; }
  done < <(raw_targets)
  python3 - "$CHAIN_DIR/config.yml" "$WORK" <<'PY' || fail=1
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
vks = cfg['genesis']['app_state']['shielded']['params']['verifying_keys']
bad = [c for c in ('action', 'membership', 'stake', 'vote', 'move') if vks.get(c) != open(f'{sys.argv[2]}/{c}.vk.b64').read()]
if bad:
    sys.exit('stale: config.yml shielded verifying_keys: ' + ', '.join(bad))
PY
  # Register circuits: exactly the manifest's variants, each with its key.
  for v in $VARIANTS; do
    cmp -s "$WORK/register/$v.vk.b64" "$REG/$v.vk.b64" || { echo "stale: $REG/$v.vk.b64" >&2; fail=1; }
  done
  for f in "$REG"/*.vk.b64; do
    [ -f "$WORK/register/$(basename "$f")" ] || { echo "not a variant: $f" >&2; fail=1; }
  done
  python3 - "$CHAIN_DIR/config.yml" "$WORK/register" <<'PY' || fail=1
import os, sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
vks = cfg['genesis']['app_state']['personhood']['params']['verifying_keys']
want = {f[:-len('.vk.b64')]: open(os.path.join(sys.argv[2], f)).read() for f in os.listdir(sys.argv[2])}
if vks != want:
    sys.exit('stale: config.yml personhood verifying_keys')
PY
  [ "$fail" -eq 0 ] || { echo "verifying keys do not match the circuits: run scripts/privacy-vks.sh" >&2; exit 1; }
  echo "verifying keys match the circuits"
  exit 0
fi

mkdir -p "$GEN"
for c in action membership stake vote move; do cp "$WORK/$c.vk.b64" "$GEN/$c.vk.b64"; done
while read -r c f; do cp "$WORK/vk-$c/vk" "$f"; done < <(raw_targets)
python3 - "$CHAIN_DIR/config.yml" "$WORK" <<'PY'
import re, sys
path, work = sys.argv[1], sys.argv[2]
s = open(path).read()
keys = {c: open(f'{work}/{c}.vk.b64').read() for c in ('action', 'membership', 'stake', 'vote', 'move')}
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
rm -f "$REG"/*.vk.b64
mkdir -p "$REG"
cp "$WORK"/register/*.vk.b64 "$REG/"
python3 - "$CHAIN_DIR/config.yml" "$WORK/register" <<'PY'
import os, re, sys
path, d = sys.argv[1], sys.argv[2]
s = open(path).read()
keys = sorted(f[:-len('.vk.b64')] for f in os.listdir(d))
body = ''.join(f'          {k}: "{open(os.path.join(d, k + ".vk.b64")).read()}"\n' for k in keys)
pat = re.compile(r'(\n    personhood:\n      params:\n(?:        #[^\n]*\n)*        verifying_keys:\n)(?:          [^\n]*\n)+', re.S)
if not pat.search(s):
    sys.exit('config.yml: personhood verifying_keys block not found')
s = pat.sub(lambda m: m.group(1) + body, s, count=1)
open(path, 'w').write(s)
PY
echo "wrote verifying keys; now: make genesis"
