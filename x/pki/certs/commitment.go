package certs

import (
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/poseidon2"
)

// DscCommitment is the public identity of a Document Signer inside the register
// circuits: Poseidon2 over its canonical public-key bytes, one field element per
// byte (ECDSA: x‖y; RSA: modulus big-endian).
//
// The circuit computes this from the key it verified the SOD signature against
// and returns it as a public output; the chain recomputes it from the
// certificate in MsgRegister and requires the two to match. Both sides must
// agree byte-for-byte, so this is the single definition — see
// poa_core::dsc_commitment for the in-circuit twin.
func DscCommitment(pubkey []byte) fr.Element {
	elems := make([]fr.Element, len(pubkey))
	for i, b := range pubkey {
		elems[i].SetUint64(uint64(b))
	}
	return poseidon2.Hash(elems)
}
