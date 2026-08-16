package ultrahonk

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyOracle drives the on-chain UltraHonk verifier against a real bb
// v5.0.0 poseidon2 proof (generated with nargo + bb 5.0.0). It confirms the
// verifier is wired correctly and sound: it accepts the valid proof and rejects
// a tampered public input.
func TestVerifyOracle(t *testing.T) {
	dir := "testdata"
	vk := readOrSkip(t, filepath.Join(dir, "vk"))
	proof := readOrSkip(t, filepath.Join(dir, "proof"))
	pub := readOrSkip(t, filepath.Join(dir, "public_inputs"))

	ok, err := VerifyRaw(vk, proof, pub)
	if err != nil {
		t.Fatalf("VerifyRaw: %v", err)
	}
	if !ok {
		t.Fatal("valid proof reported INVALID")
	}
	t.Log("valid bb v5.0.0 UltraHonk proof verified on-chain")

	// Soundness: flip a byte of the first public input -> must fail.
	bad := make([]byte, len(pub))
	copy(bad, pub)
	bad[FieldSize-1] ^= 0x01
	ok, err = VerifyRaw(vk, proof, bad)
	if err != nil {
		t.Fatalf("VerifyRaw (tampered): %v", err)
	}
	if ok {
		t.Fatal("tampered public input reported VALID — verifier is unsound")
	}
	t.Log("tampered public input correctly rejected")
}

func readOrSkip(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s missing (%v)", path, err)
	}
	return b
}
