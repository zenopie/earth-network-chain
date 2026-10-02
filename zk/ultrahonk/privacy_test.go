package ultrahonk

import (
	"path/filepath"
	"testing"
)

// privacyCircuits are the fixtures written by scripts/privacy-parity.sh
// (Go-built trees, proven with nargo + bb v5.0.0). The action circuit's are
// under testdata/orchard (TestOrchardBundles).
var privacyCircuits = []string{"membership"}

// TestVerifyPrivacyCircuits checks the chain verifier accepts the privacy
// circuits' proofs and rejects every single-input tamper, i.e. each public
// input (including signal, which no other constraint uses) is bound.
func TestVerifyPrivacyCircuits(t *testing.T) {
	for _, c := range privacyCircuits {
		t.Run(c, func(t *testing.T) {
			dir := filepath.Join("testdata", c)
			vk := readOrSkip(t, filepath.Join(dir, "vk"))
			proof := readOrSkip(t, filepath.Join(dir, "proof"))
			pub := readOrSkip(t, filepath.Join(dir, "public_inputs"))

			ok, err := VerifyRaw(vk, proof, pub)
			if err != nil || !ok {
				t.Fatalf("valid proof: ok=%v err=%v", ok, err)
			}
			for i := 0; i < len(pub)/FieldSize; i++ {
				bad := append([]byte(nil), pub...)
				bad[(i+1)*FieldSize-1] ^= 0x01
				ok, err := VerifyRaw(vk, proof, bad)
				if err != nil {
					t.Fatalf("input %d tampered: %v", i, err)
				}
				if ok {
					t.Fatalf("input %d tampered but proof VALID", i)
				}
			}
		})
	}
}

// BenchmarkVerifyPrivacy is the per-proof CheckTx cost for private txs.
func BenchmarkVerifyPrivacy(b *testing.B) {
	for _, c := range privacyCircuits {
		b.Run(c, func(b *testing.B) {
			dir := filepath.Join("testdata", c)
			vk := readOrSkipB(b, filepath.Join(dir, "vk"))
			proof := readOrSkipB(b, filepath.Join(dir, "proof"))
			pub := readOrSkipB(b, filepath.Join(dir, "public_inputs"))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if ok, err := VerifyRaw(vk, proof, pub); err != nil || !ok {
					b.Fatalf("ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
