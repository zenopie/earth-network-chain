#!/usr/bin/env bash
#
# Orchard bundles for zk/ultrahonk: build N-action bundles in Go, prove every action with
# nargo + bb v5.0.0, check each proof's public inputs equal Go's, and drop the
# proofs into zk/ultrahonk/testdata/orchard/bundle_<n>/ for the Go bundle
# verifier (TestOrchardBundles, BenchmarkOrchardBundle).
#
#   ./scripts/orchard-bundles.sh [path-to-earth-network-mobile] [n ...]
#
# Prints the action circuit's size and, per bundle, witness and prove times
# single-threaded (HARDWARE_CONCURRENCY=1) and multi-threaded.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOBILE_DIR="${1:-$CHAIN_DIR/../earth-network-mobile}"
shift || true
SIZES=("${@:-1 2 3 10}")
read -r -a SIZES <<<"${SIZES[*]}"
CIRCUITS="$(cd "$MOBILE_DIR/circuits" && pwd)"
NARGO="${NARGO:-nargo}"
BB="${BB:-bb}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; rm -f "$CIRCUITS"/action/orchard_*.toml "$CIRCUITS"/target/orchard_*.gz' EXIT

now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }

cd "$CIRCUITS"
"$NARGO" compile --package action --silence-warnings >/dev/null
"$BB" gates -b target/action.json -t noir-recursive 2>/dev/null | grep -E '"(circuit_size|acir_opcodes)"' | sed 's/^/    /'
mkdir -p "$WORK/vk"
"$BB" write_vk -b target/action.json -o "$WORK/vk" -t noir-recursive >/dev/null 2>&1

for n in "${SIZES[@]}"; do
  echo "==> bundle of $n"
  out="$WORK/b$n"
  ( cd "$CHAIN_DIR" && go run ./tools/orchardfixtures "$n" "$out" )
  dst="$CHAIN_DIR/zk/ultrahonk/testdata/orchard/bundle_$n"
  rm -rf "$dst"; mkdir -p "$dst"
  cp "$out/bundle.json" "$WORK/vk/vk" "$dst/"
  tw=0; t1=0; tm=0
  for ((i = 0; i < n; i++)); do
    a="$out/action_$i"
    cp "$a/Prover.toml" "$CIRCUITS/action/orchard_$i.toml"
    s=$(now); "$NARGO" execute --package action -p "orchard_$i" "orchard_$i" --silence-warnings >/dev/null; e=$(now)
    tw=$(echo "$tw + $e - $s" | bc)
    mkdir -p "$a/st" "$a/mt"
    s=$(now)
    HARDWARE_CONCURRENCY=1 "$BB" prove -b target/action.json -w "target/orchard_$i.gz" -k "$WORK/vk/vk" -o "$a/st" -t noir-recursive >/dev/null 2>&1
    e=$(now); t1=$(echo "$t1 + $e - $s" | bc)
    s=$(now)
    "$BB" prove -b target/action.json -w "target/orchard_$i.gz" -k "$WORK/vk/vk" -o "$a/mt" -t noir-recursive >/dev/null 2>&1
    e=$(now); tm=$(echo "$tm + $e - $s" | bc)
    cmp -s "$a/mt/public_inputs" "$a/public_inputs.expected" \
      || { echo "error: action $i public inputs differ from Go's" >&2; exit 1; }
    mkdir -p "$dst/action_$i"
    cp "$a/mt/proof" "$a/mt/public_inputs" "$dst/action_$i/"
  done
  echo "    public inputs == Go for all $n actions"
  echo "    witness (nargo execute) total: ${tw}s"
  echo "    prove, 1 thread:  total ${t1}s  per action $(echo "scale=3; $t1 / $n" | bc)s"
  echo "    prove, $(sysctl -n hw.ncpu 2>/dev/null || nproc) threads: total ${tm}s  per action $(echo "scale=3; $tm / $n" | bc)s"
  echo "    proof bytes: $(wc -c <"$dst/action_0/proof" | tr -d ' ') per action"
done
echo "done"
