package testutil

import (
	"crypto/sha256"

	"github.com/earth-network/earth/x/shielded/types"
)

// BlindCT is a stand-in amount-blind ciphertext for a minted note (the chain
// only checks its length): types.BlindCiphertextBytes bytes derived from
// label, so fixtures stay deterministic.
func BlindCT(label string) []byte {
	out := make([]byte, 0, types.BlindCiphertextBytes+sha256.Size)
	for i := byte(0); len(out) < types.BlindCiphertextBytes; i++ {
		h := sha256.Sum256(append([]byte("blind-ct/"+label+"/"), i))
		out = append(out, h[:]...)
	}
	return out[:types.BlindCiphertextBytes]
}
