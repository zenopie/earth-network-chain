package ultrahonk

import (
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"
)

// Re-audit R6: bb reduces every 32-byte proof element mod r, so an element
// x and x+r verified alike: one proof, many spellings. Every element must be
// canonical; the honest proof still verifies.
func TestProofElementAliasRefused(t *testing.T) {
	b, sighash, vk := loadBundle(t, 1)
	good := b.Actions[0].Proof
	in := b.PublicInputs(0, sighash)
	ok, err := Verify(vk, good, in)
	require.NoError(t, err)
	require.True(t, ok)

	two256 := new(big.Int).Lsh(big.NewInt(1), 256)
	tried := 0
	for off := 0; off+FieldSize <= len(good); off += FieldSize {
		v := new(big.Int).SetBytes(good[off : off+FieldSize])
		require.Negative(t, v.Cmp(fr.Modulus()), "honest element %d is canonical", off/FieldSize)
		w := new(big.Int).Add(v, fr.Modulus())
		if w.Cmp(two256) >= 0 {
			continue
		}
		p := append([]byte(nil), good...)
		w.FillBytes(p[off : off+FieldSize])
		ok, err := Verify(vk, p, in)
		require.Error(t, err, "aliased element %d", off/FieldSize)
		require.False(t, ok)
		tried++
	}
	require.Positive(t, tried)
}
