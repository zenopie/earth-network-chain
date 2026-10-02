#!/usr/bin/env bash
#
# Go <-> Noir parity for the membership circuit, plus prover measurements.
# (The action circuit's: scripts/orchard-bundles.sh and
# scripts/shielded-fixtures.sh.)
#
# For membership: tools/privacyfixtures builds the trees and
# derivations in Go and writes Prover.toml; nargo execute must accept it, bb
# proves and verifies it, and the proof's public inputs must equal the ones Go
# computed. membership-zeroed (the leaf zeroed after the path was taken) must be
# rejected. Proof, vk and public inputs land in zk/ultrahonk/testdata/<circuit>
# for the Go verifier test.
#
#   ./scripts/privacy-parity.sh [path-to-earth-network-mobile]
#
# Requires nargo and bb (v5.0.0) on PATH.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOBILE_DIR="${1:-$CHAIN_DIR/../earth-network-mobile}"
CIRCUITS="$MOBILE_DIR/circuits"

for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; rm -f "$CIRCUITS"/membership/Prover.toml' EXIT

now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }

for c in membership; do
  echo "==> $c"
  out="$WORK/$c"
  mkdir -p "$out/proof" "$out/vk"
  ( cd "$CHAIN_DIR" && go run ./tools/privacyfixtures "$c" "$out" )
  cp "$out/Prover.toml" "$CIRCUITS/$c/Prover.toml"
  ( cd "$CIRCUITS" && nargo execute --package "$c" >/dev/null )
  echo "    nargo execute: accepted"

  cd "$CIRCUITS"
  bb gates -b "target/$c.json" -t noir-recursive 2>/dev/null | grep -E '"(circuit_size|acir_opcodes)"' | sed 's/^/    /'
  bb write_vk -b "target/$c.json" -o "$out/vk" -t noir-recursive >/dev/null 2>&1
  t0=$(now)
  bb prove -b "target/$c.json" -w "target/$c.gz" -k "$out/vk/vk" -o "$out/proof" -t noir-recursive >/dev/null 2>&1
  t1=$(now)
  bb verify -k "$out/vk/vk" -p "$out/proof/proof" -i "$out/proof/public_inputs" -t noir-recursive >/dev/null 2>&1
  t2=$(now)
  echo "    prove: $(echo "$t1 - $t0" | bc) s   verify: $(echo "$t2 - $t1" | bc) s   proof: $(wc -c <"$out/proof/proof" | tr -d ' ') B   vk: $(wc -c <"$out/vk/vk" | tr -d ' ') B"
  cd "$CHAIN_DIR"

  cmp -s "$out/proof/public_inputs" "$out/public_inputs.expected" \
    || { echo "error: $c public inputs differ from Go's" >&2; exit 1; }
  echo "    public inputs == Go"

  dst="$CHAIN_DIR/zk/ultrahonk/testdata/$c"
  mkdir -p "$dst"
  cp "$out/proof/proof" "$out/proof/public_inputs" "$out/vk/vk" "$dst/"
done

echo "==> membership-zeroed (must be rejected)"
out="$WORK/mz"
( cd "$CHAIN_DIR" && go run ./tools/privacyfixtures membership-zeroed "$out" )
cp "$out/Prover.toml" "$CIRCUITS/membership/Prover.toml"
if ( cd "$CIRCUITS" && nargo execute --package membership >/dev/null 2>&1 ); then
  echo "error: zeroed leaf was accepted" >&2; exit 1
fi
echo "    rejected"
echo "done"
