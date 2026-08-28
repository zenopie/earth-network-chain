package ultrahonk

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// TestRegisterVariantProofs verifies that the chain's UltraHonk verifier accepts
// every per-DSC-algorithm register-circuit variant (RSA-2048/4096 and ECDSA on
// P-384 and the Brainpool curves) — the SOD→DSC circuits for non-P256 passports.
// Each is a distinct circuit + VK; x/personhood selects it by signature_algorithm,
// so accepting the proof here is what "the chain accepts them" means. Public inputs
// are [current_date=250101, nullifier, dsc_key].
func TestRegisterVariantProofs(t *testing.T) {
	variants := []string{
		"lean_poa_rsa2048",
		"lean_poa_rsa4096",
		"lean_poa_p384",
		"lean_poa_brainpool256",
		"lean_poa_brainpool384",
		"lean_poa_brainpool512",
	}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			dir := filepath.Join("testdata", variant)
			vk, err := os.ReadFile(filepath.Join(dir, "vk"))
			if err != nil {
				t.Skip(err)
			}
			proof, err := os.ReadFile(filepath.Join(dir, "proof"))
			if err != nil {
				t.Skip(err)
			}
			pub, err := os.ReadFile(filepath.Join(dir, "public_inputs"))
			if err != nil {
				t.Skip(err)
			}
			var pubInputs [][]byte
			for i := 0; i+32 <= len(pub); i += 32 {
				pubInputs = append(pubInputs, pub[i:i+32])
			}
			ok, err := Verify(vk, proof, pubInputs)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if !ok {
				t.Fatal("variant proof did NOT verify on chain")
			}
			// [current_date, address, nullifier, dsc_key] -- address became a
			// public input so a proof cannot be replayed from another wallet.
			if len(pubInputs) != 4 {
				t.Fatalf("expected 4 public inputs, got %d", len(pubInputs))
			}
			if cd := new(big.Int).SetBytes(pubInputs[0]); cd.String() != "250101" {
				t.Fatalf("current_date public input = %s, want 250101", cd)
			}
			bound, err := os.ReadFile(filepath.Join(dir, "expected_address"))
			if err != nil {
				t.Fatalf("read expected_address: %v", err)
			}
			if got, want := new(big.Int).SetBytes(pubInputs[1]), new(big.Int).SetBytes(bound); got.Cmp(want) != 0 {
				t.Fatalf("address public input = %s, want %s", got, want)
			}
			t.Logf("%s verified on chain", variant)
		})
	}
}
