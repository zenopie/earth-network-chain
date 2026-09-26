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
	"math/big"

	bb "github.com/burnt-labs/barretenberg-go/barretenberg"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// FieldSize is the byte length of a BN254 field element.
const FieldSize = 32

// PairingPointInputs is how many public inputs a bb v5.0.0 UltraHonk
// verifying key counts beyond the circuit's own: the aggregated pairing point
// object, eight limbs, which the proof carries itself. A key's declared count
// minus this is the number of inputs a caller supplies.
const PairingPointInputs = 8

// Verify checks a bb v5.0.0 UltraHonk proof against vk with the given public
// inputs (each a 32-byte big-endian field element). It returns true iff the
// proof is valid. A malformed vk/proof yields an error; a well-formed but
// invalid proof yields (false, nil).
//
// Each input must be exactly FieldSize bytes holding a value below the BN254
// scalar modulus, and there must be exactly as many as the key declares. The
// library checks neither. A value of p or more is reduced mod p inside the
// verifier, so p+x verifies wherever x does — two encodings of one input, and
// any caller comparing the bytes it passed in against something else is then
// comparing the wrong thing. The count went unchecked on the Go side and was
// left to the native code to notice.
func Verify(vk, proof []byte, publicInputs [][]byte) (bool, error) {
	modulus := fr.Modulus()
	for i, in := range publicInputs {
		if len(in) != FieldSize {
			return false, fmt.Errorf("public input %d is %d bytes, want %d", i, len(in), FieldSize)
		}
		if new(big.Int).SetBytes(in).Cmp(modulus) >= 0 {
			return false, fmt.Errorf("public input %d is not a canonical field element", i)
		}
	}
	v, err := bb.NewVerifierFromBytes(vk)
	if err != nil {
		return false, fmt.Errorf("parse verification key: %w", err)
	}
	defer v.Close()
	declared, err := v.NumPublicInputs()
	if err != nil {
		return false, fmt.Errorf("read verification key: %w", err)
	}
	if want := declared - PairingPointInputs; len(publicInputs) != want {
		return false, fmt.Errorf("got %d public inputs, the verification key takes %d", len(publicInputs), want)
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
