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
#
# Then the negative fixture zk/ultrahonk/testdata/orchard/over_note_max/
# (TestOrchardOutputAboveNoteMax): a balanced 2-action bundle whose first
# output is 2^63, one above a note's maximum. nargo refuses that witness, so
# it is solved by a twin of the circuit without the note bound (a range
# constraint on an existing witness: the twin's witnesses fit the real
# circuit, which the honest action 1, solved by the twin and proven by the
# real circuit, shows by verifying) and proven by bb with the real circuit.
# Its proof must not verify.
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

echo "==> bundle with an output of 2^63 (over the note maximum)"
out="$WORK/over"
( cd "$CHAIN_DIR" && go run ./tools/orchardfixtures over "$out" )
cp "$out/action_0/Prover.toml" "$CIRCUITS/action/orchard_over.toml"
if "$NARGO" execute --package action -p orchard_over orchard_over --silence-warnings >/dev/null 2>&1; then
  echo "error: the circuit accepts an output of 2^63" >&2; exit 1
fi
TWIN="$WORK/twin"
mkdir -p "$TWIN"
( cd "$CIRCUITS" && tar --exclude ./target -cf - . ) | tar -xf - -C "$TWIN"
lib="$TWIN/privacy_core/src/lib.nr"
sed -i.bak 's/(value as Field)\.assert_max_bit_size::<NOTE_VALUE_BITS>();/let _ = value;/' "$lib"
if grep -q 'assert_max_bit_size::<NOTE_VALUE_BITS>' "$lib" || ! grep -q 'let _ = value;' "$lib"; then echo "error: note bound not found in $lib" >&2; exit 1; fi
( cd "$TWIN" && "$NARGO" compile --package action --silence-warnings >/dev/null 2>&1 )
dst="$CHAIN_DIR/zk/ultrahonk/testdata/orchard/over_note_max"
rm -rf "$dst"; mkdir -p "$dst"
cp "$out/bundle.json" "$WORK/vk/vk" "$dst/"
for i in 0 1; do
  a="$out/action_$i"
  cp "$a/Prover.toml" "$TWIN/action/over_$i.toml"
  ( cd "$TWIN" && "$NARGO" execute --package action -p "over_$i" "over_$i" --silence-warnings >/dev/null )
  mkdir -p "$a/p"
  "$BB" prove -b target/action.json -w "$TWIN/target/over_$i.gz" -k "$WORK/vk/vk" -o "$a/p" -t noir-recursive >/dev/null 2>&1 \
    || { echo "error: bb produced no proof for action $i (the Go test needs one)" >&2; exit 1; }
  cmp -s "$a/p/public_inputs" "$a/public_inputs.expected" \
    || { echo "error: action $i public inputs differ from Go's" >&2; exit 1; }
  if "$BB" verify -k "$WORK/vk/vk" -p "$a/p/proof" -i "$a/p/public_inputs" -t noir-recursive >/dev/null 2>&1; then ok=1; else ok=0; fi
  if [ "$i" = 0 ] && [ "$ok" = 1 ]; then echo "error: a proof of an output of 2^63 verifies" >&2; exit 1; fi
  if [ "$i" = 1 ] && [ "$ok" = 0 ]; then echo "error: the twin's honest witness does not fit the real circuit" >&2; exit 1; fi
  mkdir -p "$dst/action_$i"
  cp "$a/p/proof" "$a/p/public_inputs" "$dst/action_$i/"
done
echo "    action 0 (output 2^63): proof refused; action 1 (honest, twin witness): verifies"
echo "done"
