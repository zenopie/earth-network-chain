package ultrahonk

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/earth-network/earth/x/pki/certs"
)

// TestDscCommitmentMatchesCircuit checks, for every register-circuit variant,
// that the commitment the chain computes from a Document Signer's public key is
// the same value the circuit put in its proof.
//
// This is the hinge of the whole design. The circuit proves "the passport was
// signed by the key committed to here", and x/pki separately proves "that key
// belongs to a trusted signer". If the two sides hashed the key even slightly
// differently — a coordinate padded to the wrong length on one curve, say — no
// registration would ever be accepted, and only for that curve.
//
// Covering it here rather than through a certificate matters for Brainpool:
// crypto/x509 cannot encode those curves, so no test certificate can be built
// for them, but the key bytes and the proof are enough to check the agreement.
func TestDscCommitmentMatchesCircuit(t *testing.T) {
	variants := []string{
		"lean_poa",
		"lean_poa_p384",
		"lean_poa_rsa2048",
		"lean_poa_rsa4096",
		"lean_poa_brainpool256",
		"lean_poa_brainpool384",
		"lean_poa_brainpool512",
	}
	// Public signals are [current_date, nullifier, dsc_key].
	const dscKeyIndex = 2

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			dir := filepath.Join("testdata", variant)
			pubkey, err := os.ReadFile(filepath.Join(dir, "dsc_pubkey"))
			if err != nil {
				t.Skip(err)
			}
			signals, err := os.ReadFile(filepath.Join(dir, "public_inputs"))
			if err != nil {
				t.Skip(err)
			}
			if len(signals) < (dscKeyIndex+1)*32 {
				t.Fatalf("expected at least %d public signals, got %d", dscKeyIndex+1, len(signals)/32)
			}

			fromProof := new(big.Int).SetBytes(signals[dscKeyIndex*32 : (dscKeyIndex+1)*32])

			commitment := certs.DscCommitment(pubkey)
			bz := commitment.Bytes()
			fromChain := new(big.Int).SetBytes(bz[:])

			if fromChain.Cmp(fromProof) != 0 {
				t.Fatalf("commitment mismatch over a %d-byte key:\n  chain   = %s\n  circuit = %s",
					len(pubkey), fromChain, fromProof)
			}
		})
	}
}
