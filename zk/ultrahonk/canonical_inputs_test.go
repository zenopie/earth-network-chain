package ultrahonk

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func loadLean(t *testing.T) (vk, proof []byte, inputs [][]byte) {
	t.Helper()
	dir := "testdata/lean_poa"
	vk, err := os.ReadFile(filepath.Join(dir, "vk"))
	if err != nil {
		t.Skip("lean_poa fixture missing")
	}
	proof, _ = os.ReadFile(filepath.Join(dir, "proof"))
	pub, _ := os.ReadFile(filepath.Join(dir, "public_inputs"))
	for i := 0; i < len(pub); i += FieldSize {
		inputs = append(inputs, append([]byte(nil), pub[i:i+FieldSize]...))
	}
	return vk, proof, inputs
}

// TestNonCanonicalInputIsRefused: x + p is the same field element as x, and
// used to verify wherever x did.
func TestNonCanonicalInputIsRefused(t *testing.T) {
	vk, proof, inputs := loadLean(t)
	if ok, err := Verify(vk, proof, inputs); !ok || err != nil {
		t.Fatalf("fixture: ok=%v err=%v", ok, err)
	}
	for i := range inputs {
		shifted := new(big.Int).Add(new(big.Int).SetBytes(inputs[i]), fr.Modulus())
		if shifted.BitLen() > 8*FieldSize {
			continue // does not fit in 32 bytes, so not representable anyway
		}
		alias := make([][]byte, len(inputs))
		copy(alias, inputs)
		alias[i] = shifted.FillBytes(make([]byte, FieldSize))
		if ok, err := Verify(vk, proof, alias); ok || err == nil {
			t.Errorf("input %d + p: ok=%v err=%v, want refusal", i, ok, err)
		}
	}
}

func TestInputCountIsPinned(t *testing.T) {
	vk, proof, inputs := loadLean(t)
	for name, in := range map[string][][]byte{
		"one short": inputs[:len(inputs)-1],
		"one extra": append(append([][]byte(nil), inputs...), make([]byte, FieldSize)),
	} {
		if ok, err := Verify(vk, proof, in); ok || err == nil {
			t.Errorf("%s: ok=%v err=%v, want refusal", name, ok, err)
		}
	}
}
