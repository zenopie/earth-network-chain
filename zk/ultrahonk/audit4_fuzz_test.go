package ultrahonk

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Audit 4 (core PoC, ported): mutate every 32-byte element of a valid action proof to canonical
// junk (0, 1, 2, r-1, random < r) and check bb returns instead of aborting.
func TestAudit4ProofElementJunkNoCrash(t *testing.T) {
	b, sighash, vk := loadBundle(t, 1)
	good := b.Actions[0].Proof
	in := b.PublicInputs(0, sighash)
	rng := rand.New(rand.NewSource(1))
	rm1 := new(big.Int).Sub(fr.Modulus(), big.NewInt(1))
	vals := func() []*big.Int {
		r := new(big.Int).Rand(rng, fr.Modulus())
		return []*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(2), rm1, r}
	}
	errs, falses := 0, 0
	for off := 0; off+FieldSize <= len(good); off += FieldSize {
		for _, v := range vals() {
			p := append([]byte(nil), good...)
			v.FillBytes(p[off : off+FieldSize])
			ok, err := Verify(vk, p, in)
			if ok && err == nil && v.Cmp(new(big.Int).SetBytes(good[off:off+FieldSize])) != 0 {
				t.Errorf("element %d value %s verified", off/FieldSize, v)
			}
			if err != nil {
				errs++
			} else if !ok {
				falses++
			}
		}
	}
	// whole-proof junk
	for i := 0; i < 20; i++ {
		p := make([]byte, len(good))
		for off := 0; off < len(p); off += FieldSize {
			new(big.Int).Rand(rng, fr.Modulus()).FillBytes(p[off : off+FieldSize])
		}
		_, _ = Verify(vk, p, in)
	}
	t.Logf("errs=%d false=%d", errs, falses)
}
