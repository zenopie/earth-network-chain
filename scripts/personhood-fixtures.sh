#!/usr/bin/env bash
#
# Real proofs for x/personhood's and x/assembly's app tests.
#
#   ./scripts/personhood-fixtures.sh [path-to-earth-network-mobile circuits dir] [passports|app|gas|all]
#
# passports: one lean_poa passport proof per x/personhood/testutil
#   Registration, bound (address input) to its RegistrationBinding, for its
#   document number and current_date; each with its own fresh CSCA and DSC.
#   -> x/personhood/testdata/passports/<name>/
# app: reruns the app tests in proving mode (EARTH_PROVE_CIRCUITS): they drive
#   a deterministic chain and prove every membership and fee-transfer witness
#   against its real trees as they go.
#   -> x/personhood/testdata/app/
# gas: the transparent gas grant's membership proof (GasScope, bound to an
#   address), for x/personhood/keeper's CheckGasMembership test.
#   -> x/personhood/testdata/gas/
#
# Passport proofs carry random DSC keys, which the identity leaves commit to,
# so regenerating passports means regenerating app too (all, the default).
# The circuits are copied to a temp dir; the mobile checkout is never written
# to. Requires nargo and bb (v5.0.0) on PATH or in ~/.nargo/bin, ~/.bb.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CIRCUITS_SRC="$(cd "${1:-$CHAIN_DIR/../earth-network-mobile/circuits}" && pwd)"
WHAT="${2:-all}"
export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"

for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
for c in lean_poa membership transfer; do
  [ -d "$CIRCUITS_SRC/$c" ] || { echo "error: no $c circuit under $CIRCUITS_SRC" >&2; exit 1; }
done

if [ "$WHAT" = all ] || [ "$WHAT" = passports ]; then
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  cp -R "$CIRCUITS_SRC" "$WORK/circuits"
  rm -rf "$WORK/circuits/target"
  C="$WORK/circuits"
  ( cd "$C" && nargo compile --package lean_poa >/dev/null \
      && bb write_vk -b target/lean_poa.json -o "$WORK/vk" -t noir-recursive >/dev/null 2>&1 )
  DST="$CHAIN_DIR/x/personhood/testdata/passports"
  rm -rf "$DST"
  for name in A1 A2 B C1 C2; do
    echo "==> passport $name"
    out="$WORK/$name"
    # shellcheck disable=SC2046
    ( cd "$CHAIN_DIR" && go run ./tools/poafixtures lean_poa "$out" \
        $(go run ./tools/privacyfixtures passport "$name") >/dev/null )
    cp "$out/Prover.toml" "$C/lean_poa/Prover.toml"
    ( cd "$C" && nargo execute --package lean_poa >/dev/null \
        && bb prove -b target/lean_poa.json -w target/lean_poa.gz -k "$WORK/vk/vk" \
             -o "$out/proof" -t noir-recursive >/dev/null 2>&1 \
        && bb verify -k "$WORK/vk/vk" -p "$out/proof/proof" -i "$out/proof/public_inputs" \
             -t noir-recursive >/dev/null 2>&1 )
    mkdir -p "$DST/$name"
    cp "$out/proof/proof" "$out/proof/public_inputs" "$DST/$name/"
    cp "$out/csca.der" "$out/dsc.der" "$out/expected_dsc_key" "$out/expected_nullifier" "$DST/$name/"
  done
  cp "$WORK/vk/vk" "$DST/lean_poa.vk"
fi

if [ "$WHAT" = all ] || [ "$WHAT" = app ]; then
  echo "==> app tests, proving"
  rm -f "$CHAIN_DIR"/x/personhood/testdata/app/*.proof
  ( cd "$CHAIN_DIR" && EARTH_PROVE_CIRCUITS="$CIRCUITS_SRC" \
      go test ./app -run 'TestPrivatePersonhood' -count=1 -timeout 60m )
fi
if [ "$WHAT" = all ] || [ "$WHAT" = gas ]; then
  echo "==> gas membership, proving"
  rm -f "$CHAIN_DIR"/x/personhood/testdata/gas/*.proof
  ( cd "$CHAIN_DIR" && EARTH_PROVE_CIRCUITS="$CIRCUITS_SRC" \
      go test ./x/personhood/keeper -run 'TestCheckGasMembership' -count=1 )
fi
echo done
