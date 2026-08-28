#!/usr/bin/env bash
#
# Regenerates the passport register-circuit test fixtures for every variant:
# witness inputs, verifying key, proof, public signals, and the canonical DSC
# public key the commitment is taken over.
#
# Run after any change to circuits/** or to the DSC commitment, then commit the
# refreshed testdata:
#
#   ./scripts/regen-poa-fixtures.sh [path-to-earth-network-mobile]
#
# Requires nargo and bb on PATH:
#   export PATH="$HOME/.nargo/bin:$HOME/.bb:$PATH"
#
# Everything for a variant must come from ONE run — Go's ECDSA hedges nonces, so
# a fresh key is generated each time and a proof will not match key bytes from a
# different invocation.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOBILE_DIR="${1:-$CHAIN_DIR/../earth-network-mobile}"
CIRCUITS="$MOBILE_DIR/circuits"

for bin in nargo bb; do
  command -v "$bin" >/dev/null || { echo "error: $bin not on PATH" >&2; exit 1; }
done
[ -d "$CIRCUITS" ] || { echo "error: no circuits dir at $CIRCUITS" >&2; exit 1; }

VARIANTS=(
  lean_poa
  lean_poa_p384
  lean_poa_rsa2048
  lean_poa_rsa4096
  lean_poa_brainpool256
  lean_poa_brainpool384
  lean_poa_brainpool512
)

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

for v in "${VARIANTS[@]}"; do
  echo "==> $v"
  out="$WORK/$v"
  mkdir -p "$out/proof"

  ( cd "$CHAIN_DIR" && go run ./tools/poafixtures "$v" "$out" >/dev/null )
  cp "$out/Prover.toml" "$CIRCUITS/$v/Prover.toml"

  ( cd "$CIRCUITS" \
      && nargo execute --package "$v" >/dev/null \
      && bb write_vk -b "target/$v.json" -o "$out/vk" -t noir-recursive >/dev/null \
      && bb prove -b "target/$v.json" -w "target/$v.gz" -k "$out/vk/vk" \
                  -o "$out/proof" -t noir-recursive >/dev/null )

  # zk/ultrahonk: verifier-level fixtures for every variant.
  dst="$CHAIN_DIR/zk/ultrahonk/testdata/$v"
  mkdir -p "$dst"
  cp "$out/proof/proof" "$out/proof/public_inputs" "$out/vk/vk" "$dst/"
  cp "$out/dsc_pubkey" "$out/expected_dsc_key" "$out/expected_nullifier" "$dst/"
  cp "$out/expected_address" "$dst/"

  # x/personhood: lean_poa additionally carries the certificate chain, for the
  # end-to-end test against a real x/pki keeper.
  #
  # This said x/caretaker until 2026-08-27, which is the module's old name. The
  # copy went on succeeding -- mkdir -p happily created the dead path -- so the
  # module's fixtures silently stopped being refreshed while the script kept
  # reporting success. They were three months stale by the time the public input
  # vector changed shape and the tests started failing against them.
  if [ "$v" = "lean_poa" ]; then
    ct="$CHAIN_DIR/x/personhood/keeper/testdata/lean_poa"
    mkdir -p "$ct"
    cp "$out/proof/proof" "$out/proof/public_inputs" "$out/vk/vk" "$ct/"
    cp "$out/dsc_pubkey" "$out/expected_dsc_key" "$out/expected_nullifier" "$ct/"
    cp "$out/expected_address" "$ct/"
    cp "$out/csca.der" "$out/dsc.der" "$ct/"
  fi

  # Prover.toml is a generated witness, not a source file.
  rm -f "$CIRCUITS/$v/Prover.toml"
done

echo "done — refreshed fixtures for ${#VARIANTS[@]} variants"
