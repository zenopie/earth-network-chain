#!/usr/bin/env bash
#
# Real action proofs for x/shielded's tests.
#
# x/shielded/testutil's scenario (shields, then MsgSend bundles of 1, 2, 3
# and 10 actions, built and signed in Go, sighashes bound to
# testutil.ChainID) and the keeper and app tests that replay it fetch every
# action proof from x/shielded/testdata/proofs, keyed by the action's public
# inputs. This script empties that cache and re-runs those tests with
# EARTH_CIRCUITS set, so every proof they need is proven with nargo + bb
# against x/shielded/testdata/action.vk (bb's public inputs must equal the
# chain's, byte for byte) and written back. With EARTH_CIRCUITS set, the
# -G inflation test also runs the circuit on the forged witness and requires
# it refused.
#
#   ./scripts/shielded-fixtures.sh [path-to-earth-network-mobile circuits dir]
#
# Run scripts/privacy-vks.sh first after a circuit change. Requires nargo and
# bb (v5.0.0) on PATH or in ~/.nargo/bin, ~/.bb.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CIRCUITS_SRC="$(cd "${1:-$CHAIN_DIR/../earth-network-mobile/circuits}" && pwd)"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"

for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
[ -d "$CIRCUITS_SRC/action" ] || { echo "error: no action circuit under $CIRCUITS_SRC" >&2; exit 1; }

DST="$CHAIN_DIR/x/shielded/testdata/proofs"
rm -rf "$DST"
cd "$CHAIN_DIR"
# One package at a time: both share the cache.
EARTH_CIRCUITS="$CIRCUITS_SRC" go test -count=1 -p 1 ./x/shielded/... ./app -run 'TestScenario|TestSighash|TestInflation|TestNegated|TestCheckPrivate|TestHandler|TestGenesisRoundTrip|TestQueries|TestShielded|TestAudit3UnshieldIntoStakingModuleRefused|TestAudit3AppMempool'
echo "done: $(ls "$DST" | wc -l | tr -d ' ') proofs in $DST"
