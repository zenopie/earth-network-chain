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
// Each is a distinct circuit + VK; the caretaker selects it by signature_algorithm,
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
			if len(pubInputs) != 3 {
				t.Fatalf("expected 3 public inputs, got %d", len(pubInputs))
			}
			if cd := new(big.Int).SetBytes(pubInputs[0]); cd.String() != "250101" {
				t.Fatalf("current_date public input = %s, want 250101", cd)
			}
			t.Logf("%s verified on chain", variant)
		})
	}
}
