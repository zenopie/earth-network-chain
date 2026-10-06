#!/usr/bin/env bash
#
# Regenerates the passport register-circuit verifier fixtures, one per variant
# in the mobile repo's circuits/variants.json: proof, public inputs and
# verifying key, plus the synthetic passport's DSC and CSCA certificates and
# the values the proof must carry.
#
# The witness is the variant's shared fixture (circuits/fixtures/<variant>/,
# written by circuits/tools/variants.py gen): the same synthetic passport the
# circuit's nargo tests and both wallets' tests use, so the chain checks the
# very proofs the wallets' witnesses produce.
#
#   ./scripts/regen-poa-fixtures.sh [path-to-earth-network-mobile]
#
# Requires nargo (1.0.0-beta.22) and bb (5.0.0) on PATH or in ~/.nargo/bin,
# ~/.bb. The mobile checkout is never written to.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOBILE_DIR="${1:-$CHAIN_DIR/../earth-network-mobile}"
CIRCUITS_SRC="$MOBILE_DIR/circuits"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"

for bin in nargo bb python3; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
[ -f "$CIRCUITS_SRC/variants.json" ] || { echo "error: no circuits/variants.json under $MOBILE_DIR" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -R "$CIRCUITS_SRC" "$WORK/circuits"
rm -rf "$WORK/circuits/target"
C="$WORK/circuits"
VARIANTS=$(python3 -c 'import json,sys; print(" ".join(v["id"] for v in json.load(open(sys.argv[1]))["variants"]))' "$C/variants.json")

rm -rf "$CHAIN_DIR"/zk/ultrahonk/testdata/lean_poa*
for v in $VARIANTS; do
  echo "==> $v"
  fx="$C/fixtures/$v"
  out="$WORK/out/$v"
  mkdir -p "$out"
  cp "$fx/Prover.toml" "$C/$v/Prover.toml"
  ( cd "$C" \
      && nargo execute --package "$v" >/dev/null \
      && bb write_vk -b "target/$v.json" -o "$out" -t noir-recursive >/dev/null 2>&1 \
      && bb prove -b "target/$v.json" -w "target/$v.gz" -k "$out/vk" -o "$out" -t noir-recursive >/dev/null 2>&1 \
      && bb verify -k "$out/vk" -p "$out/proof" -i "$out/public_inputs" -t noir-recursive >/dev/null 2>&1 )
  dst="$CHAIN_DIR/zk/ultrahonk/testdata/$v"
  mkdir -p "$dst"
  cp "$out/proof" "$out/public_inputs" "$out/vk" "$fx/dsc.der" "$fx/csca.der" "$dst/"
  python3 - "$fx/expected.json" "$dst" <<'PY'
import json, sys
e = json.load(open(sys.argv[1]))
open(sys.argv[2] + '/expected_nullifier', 'w').write(e['nullifier'])
open(sys.argv[2] + '/expected_dsc_key', 'w').write(e['dsc_key'])
open(sys.argv[2] + '/expected_idc', 'w').write(e['idc'])
open(sys.argv[2] + '/expected_address', 'wb').write(bytes.fromhex(e['address'][2:]))
PY
done
echo "done: verifier fixtures for $(echo $VARIANTS | wc -w | tr -d ' ') variants"
