#!/usr/bin/env bash
#
# Real proofs for x/shieldedstaking's app tests.
#
# Those tests run a deterministic chain (fixed keys, genesis time and block
# times) and read each proof they need from
# x/shieldedstaking/testdata/proofs/action-<hash of its public inputs>.proof.
# A proof's public inputs depend on what the chain computed before it (a derth
# note's value follows the rate), so they are recorded from a run rather than
# written down: with EARTH_CIRCUITS set, a missing proof is proven with nargo +
# bb against the committed verifying keys and written.
#
#   ./scripts/staking-fixtures.sh [path-to-earth-network-mobile/circuits]
#
# Stale proofs (for inputs no test produces any more) are removed first, so the
# directory holds exactly what the tests use. Requires nargo and bb (v5.0.0).
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CIRCUITS="${1:-$CHAIN_DIR/../earth-network-mobile/circuits}"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"
for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
[ -d "$CIRCUITS/action" ] || { echo "error: no action circuit under $CIRCUITS" >&2; exit 1; }
CIRCUITS="$(cd "$CIRCUITS" && pwd)"

PROOFS="$CHAIN_DIR/x/shieldedstaking/testdata/proofs"
rm -rf "$PROOFS"
mkdir -p "$PROOFS"
cd "$CHAIN_DIR"
EARTH_CIRCUITS="$CIRCUITS" go test ./app/ -count=1 -timeout 60m \
  -run 'TestPrivateStaking|TestTransparentStakingBlocked|TestStakeVote|TestGroundworks|TestSelfBond|TestStakeNotes|TestEpochEndDefers|TestVestingAccount|TestCompoundingNever|TestRedelegate'
echo "done: $(ls "$PROOFS" | wc -l | tr -d ' ') proofs in $PROOFS"
