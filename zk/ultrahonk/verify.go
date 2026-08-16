// Package ultrahonk verifies Barretenberg (bb v5.0.0) UltraHonk proofs on-chain,
// via CGo bindings to Barretenberg's own verifier (github.com/burnt-labs/
// barretenberg-go, vendored under third_party with the native lib rebuilt against
// bb v5.0.0). This is the verifier for zkPassport-style Noir proofs.
//
// Flavor: default poseidon2 (UltraZKFlavor) — the natural choice for a non-EVM
// chain and zkPassport's internal recursive format. Proofs and verifying keys are
// Barretenberg binary blobs (`bb prove` / `bb write_vk`); public inputs are
// 32-byte big-endian field elements.
//
// NOTE: requires CGO_ENABLED=1 and the native libbarretenberg.a for the build
// platform (third_party/barretenberg-go/lib/<platform>/). Only this package (and
// anything importing it) needs CGo; the rest of the chain stays pure Go.
package ultrahonk

import (
	"fmt"

	bb "github.com/burnt-labs/barretenberg-go/barretenberg"
)

// FieldSize is the byte length of a BN254 field element.
const FieldSize = 32

// Verify checks a bb v5.0.0 UltraHonk proof against vk with the given public
// inputs (each a 32-byte big-endian field element). It returns true iff the
// proof is valid. A malformed vk/proof yields an error; a well-formed but
// invalid proof yields (false, nil).
func Verify(vk, proof []byte, publicInputs [][]byte) (bool, error) {
	v, err := bb.NewVerifierFromBytes(vk)
	if err != nil {
		return false, fmt.Errorf("parse verification key: %w", err)
	}
	p, err := bb.ParseProof(proof)
	if err != nil {
		return false, fmt.Errorf("parse proof: %w", err)
	}
	return v.VerifyWithBytes(p, publicInputs)
}

// VerifyRaw is like Verify but takes the public inputs as one concatenated
// buffer of 32-byte big-endian field elements (the layout of bb's
// `public_inputs` file).
func VerifyRaw(vk, proof, publicInputs []byte) (bool, error) {
	if len(publicInputs)%FieldSize != 0 {
		return false, fmt.Errorf("public inputs length %d not a multiple of %d", len(publicInputs), FieldSize)
	}
	chunks := make([][]byte, 0, len(publicInputs)/FieldSize)
	for i := 0; i+FieldSize <= len(publicInputs); i += FieldSize {
		c := make([]byte, FieldSize)
		copy(c, publicInputs[i:i+FieldSize])
		chunks = append(chunks, c)
	}
	return Verify(vk, proof, chunks)
}
