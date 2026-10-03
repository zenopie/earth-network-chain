package testutil

import (
	"crypto/sha256"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// NoteCT is a stand-in v1 note ciphertext for a bundle output (the chain
// only checks its length): types.NoteCiphertextBytes deterministic bytes.
func NoteCT(label string) []byte { return padCT("note-ct/"+label, types.NoteCiphertextBytes) }

// StakeCT is a stand-in wallet stake ciphertext for a stake proof's output:
// privacy.WalletStakeCiphertextBytes deterministic bytes.
func StakeCT(label string) []byte {
	return padCT("stake-ct/"+label, privacy.WalletStakeCiphertextBytes)
}

func padCT(label string, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	for i := byte(0); len(out) < n; i++ {
		h := sha256.Sum256(append([]byte(label+"/"), i))
		out = append(out, h[:]...)
	}
	return out[:n]
}

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
