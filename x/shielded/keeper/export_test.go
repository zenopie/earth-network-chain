package keeper

// WithProofVerifier is k verifying proofs with fn (to count verifications).
func (k Keeper) WithProofVerifier(fn func(vk, proof []byte, publicInputs [][]byte) (bool, error)) Keeper {
	k.proofVerifier = fn
	return k
}
