package ultrahonk

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkVerify measures one UltraHonk verification against the real
// registration fixtures. It exists to ground x/personhood's
// proof_verification_gas param in a measured number rather than a guess: the
// charge has to cover the slowest circuit governance has keys for, so re-run
// this whenever the verifying keys change and re-derive the param from the
// worst case here.
func BenchmarkVerify(b *testing.B) {
	for _, dir := range []string{
		"testdata",
		"testdata/lean_poa",
		"testdata/lean_poa_p384",
		"testdata/lean_poa_rsa2048",
		"testdata/lean_poa_rsa4096",
		"testdata/lean_poa_brainpool256",
		"testdata/lean_poa_brainpool384",
		"testdata/lean_poa_brainpool512",
	} {
		b.Run(filepath.Base(dir), func(b *testing.B) {
			vk := readOrSkipB(b, filepath.Join(dir, "vk"))
			proof := readOrSkipB(b, filepath.Join(dir, "proof"))
			pub := readOrSkipB(b, filepath.Join(dir, "public_inputs"))

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := VerifyRaw(vk, proof, pub); err != nil {
					b.Fatalf("VerifyRaw: %v", err)
				}
			}
		})
	}
}

func readOrSkipB(b *testing.B, path string) []byte {
	b.Helper()
	v, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("fixture %s missing (%v)", path, err)
	}
	return v
}
