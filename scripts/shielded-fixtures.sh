#!/usr/bin/env bash
#
# Real transfer proofs for x/shielded's tests.
#
# tools/privacyfixtures shielded writes a Prover.toml per transfer in
# x/shielded/testutil's deterministic scenario (trees and derivations built in
# Go, signals bound to testutil.ChainID). This script proves each one with the
# transfer circuit and bb, checks the proof's public inputs equal the ones Go
# computed, and writes
#
#   x/shielded/testdata/transfer.vk
#   x/shielded/testdata/<transfer>/{proof,public_inputs}
#
#   ./scripts/shielded-fixtures.sh [path-to-earth-network-mobile circuits dir]
#
# The circuits are copied to a temp dir first, so the mobile checkout is never
# written to. Requires nargo and bb (v5.0.0) on PATH or in ~/.nargo/bin, ~/.bb.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CIRCUITS_SRC="${1:-$CHAIN_DIR/../earth-network-mobile/circuits}"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"

for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
[ -d "$CIRCUITS_SRC/transfer" ] || { echo "error: no transfer circuit under $CIRCUITS_SRC" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -R "$CIRCUITS_SRC" "$WORK/circuits"
rm -rf "$WORK/circuits/target"
CIRCUITS="$WORK/circuits"

( cd "$CHAIN_DIR" && go run ./tools/privacyfixtures shielded "$WORK/fx" )

cd "$CIRCUITS"
nargo compile --package transfer >/dev/null
mkdir -p "$WORK/vk"
bb write_vk -b target/transfer.json -o "$WORK/vk" -t noir-recursive >/dev/null 2>&1

DST="$CHAIN_DIR/x/shielded/testdata"
mkdir -p "$DST"
cp "$WORK/vk/vk" "$DST/transfer.vk"

for dir in "$WORK"/fx/*/; do
  name="$(basename "$dir")"
  echo "==> $name"
  cp "$dir/Prover.toml" transfer/Prover.toml
  nargo execute --package transfer >/dev/null
  mkdir -p "$WORK/proof/$name"
  bb prove -b target/transfer.json -w target/transfer.gz -k "$WORK/vk/vk" -o "$WORK/proof/$name" -t noir-recursive >/dev/null 2>&1
  bb verify -k "$WORK/vk/vk" -p "$WORK/proof/$name/proof" -i "$WORK/proof/$name/public_inputs" -t noir-recursive >/dev/null 2>&1
  cmp -s "$WORK/proof/$name/public_inputs" "$dir/public_inputs.expected" \
    || { echo "error: $name public inputs differ from Go's" >&2; exit 1; }
  echo "    proved, verified, public inputs == Go"
  mkdir -p "$DST/$name"
  cp "$WORK/proof/$name/proof" "$WORK/proof/$name/public_inputs" "$DST/$name/"
done
echo "done: $DST"
