package types

import "context"

// DscRevocationListener is notified when governance revokes a Document Signer.
//
// It exists so revocation can be a single act. Withdrawing trust from a signer
// and retiring what that signer produced are the same decision, and splitting
// them into two governance votes would leave a compromised signer's
// registrations voting for however long the second vote takes.
type DscRevocationListener interface {
	// OnDscRevoked is called with the signer's Poseidon2 commitment, the
	// identity registrations are recorded under. It must be cheap: it runs
	// inside the revocation transaction, so it should record the intent and
	// leave the work to a bounded per-block sweep.
	OnDscRevoked(ctx context.Context, commitment []byte) error
}
