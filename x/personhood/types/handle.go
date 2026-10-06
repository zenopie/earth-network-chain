package types

import (
	"fmt"

	"github.com/earth-network/earth/zk/privacy"
)

// Handle format: lowercase [a-z0-9-], HandleMinLen to HandleMaxLen
// characters, no leading or trailing dash. One spelling per handle (no case
// folding), so the chain and every wallet agree on its bytes.
const (
	HandleMinLen = 3
	HandleMaxLen = 32

	// DefaultHandleRenewalSeconds is the default owner-only renewal period
	// after a handle's lease ends (Params.handle_renewal_seconds).
	DefaultHandleRenewalSeconds = 30 * 24 * 60 * 60

	// DefaultHandleLeaseSeconds is the default lease (Params.handle_lease_seconds).
	DefaultHandleLeaseSeconds = 365 * 24 * 60 * 60

	// HandleQueryDefaultLimit and HandleQueryMaxLimit page Query/Handles.
	HandleQueryDefaultLimit = 100
	HandleQueryMaxLimit     = 1000
)

// ValidateHandle checks a handle's format.
func ValidateHandle(h string) error {
	if len(h) < HandleMinLen || len(h) > HandleMaxLen {
		return fmt.Errorf("a handle is %d..%d characters", HandleMinLen, HandleMaxLen)
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("handle %q: only a-z, 0-9 and -", h)
		}
	}
	if h[0] == '-' || h[len(h)-1] == '-' {
		return fmt.Errorf("handle %q: no leading or trailing dash", h)
	}
	return nil
}

// Address is the record's shielded address.
func (h Handle) Address() (privacy.ShieldedAddress, error) {
	var a privacy.ShieldedAddress
	pk, err := privacy.FieldFromBytes(h.OwnerPk)
	if err != nil {
		return a, fmt.Errorf("handle %q: owner_pk: %w", h.Handle, err)
	}
	if len(h.EkPub) != 32 {
		return a, fmt.Errorf("handle %q: ek_pub must be 32 bytes", h.Handle)
	}
	a.OwnerPK = pk
	copy(a.EKPub[:], h.EkPub)
	return a, nil
}

// validateHandles checks genesis handles: well formed, a valid address and
// nullifier, an expiry, no handle twice, one handle per nullifier.
func validateHandles(hs []Handle) error {
	seen, nfs := map[string]bool{}, map[string]bool{}
	for _, h := range hs {
		if err := ValidateHandle(h.Handle); err != nil {
			return err
		}
		if seen[h.Handle] {
			return fmt.Errorf("handle %q listed twice", h.Handle)
		}
		seen[h.Handle] = true
		if _, err := h.Address(); err != nil {
			return err
		}
		if _, err := privacy.FieldFromBytes(h.Nullifier); err != nil {
			return fmt.Errorf("handle %q: nullifier: %w", h.Handle, err)
		}
		if nfs[string(h.Nullifier)] {
			return fmt.Errorf("handle %q: its nullifier holds another handle", h.Handle)
		}
		nfs[string(h.Nullifier)] = true
		if h.ExpiresAt <= 0 {
			return fmt.Errorf("handle %q has no expiry", h.Handle)
		}
	}
	return nil
}

// Handle events and attributes.
const (
	EventTypeHandleBound    = "handle_bound"
	EventTypeHandleReleased = "handle_released"
	AttributeKeyHandle      = "handle"
	AttributeKeyAddress     = "address"
	AttributeKeyNullifier   = "nullifier"
	AttributeKeyExpiresAt   = "expires_at"
	// AttributeKeyOwner is the handle-scope nullifier that holds (or, on
	// handle_released, held) the handle, hex: what Query/Handle reports as
	// owner.
	AttributeKeyOwner = "owner"
)
