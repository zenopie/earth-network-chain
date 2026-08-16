package ultrahonk

import (
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"
)

// TestLeanPoaDeviceProof verifies a REAL proof of the large lean_poa passport
// circuit (~130k gates) generated on the user's Pixel 6 by noir_android (bb
// v5.0.0 final, barretenberg-rs 5.0.0) against the chain verifier (bb v5.0.0
// final). This confirms the full on-device -> on-chain loop for the actual
// circuit that guards registration, not just the toy e2e circuit — and that the
// prover/chain bb versions are aligned on final (no nightly skew).
func TestLeanPoaDeviceProof(t *testing.T) {
	ph, err := os.ReadFile("testdata/lean_device_proof.hex")
	if err != nil {
		t.Skip(err)
	}
	vh, err := os.ReadFile("testdata/lean_device_vk.hex")
	if err != nil {
		t.Skip(err)
	}
	vk, _ := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(vh), "0x")))
	flat, _ := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(ph), "0x")))

	// noir_android flattened proof = [4B field-count prefix][numPub*32 public
	// inputs][proof body]. lean_poa has 3 public inputs: registry_root,
	// current_date, nullifier.
	const numPub = 3
	body := flat[4:]
	var pub [][]byte
	for i := 0; i < numPub; i++ {
		pub = append(pub, body[i*32:(i+1)*32])
	}
	proof := body[numPub*32:]

	ok, err := Verify(vk, proof, pub)
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !ok {
		t.Fatal("device lean_poa proof did NOT verify on chain — bb version skew")
	}

	// Sanity-check the public inputs decode to the expected registration data.
	currentDate := new(big.Int).SetBytes(pub[1])
	if currentDate.String() != "250101" {
		t.Fatalf("current_date public input = %s, want 250101", currentDate)
	}
	nullifier := new(big.Int).SetBytes(pub[2])
	t.Logf("device lean_poa proof VERIFIED on chain; current_date=%s nullifier=%s...",
		currentDate, nullifier.String()[:12])
}
