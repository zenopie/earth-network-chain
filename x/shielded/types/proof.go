package types

import "fmt"

// CheckProofLength refuses a proof that is not exactly ProofBytes long.
// Callers wrap the error in their own module's.
func CheckProofLength(proof []byte) error {
	if len(proof) != ProofBytes {
		return fmt.Errorf("proof is %d bytes, must be exactly %d", len(proof), ProofBytes)
	}
	return nil
}
