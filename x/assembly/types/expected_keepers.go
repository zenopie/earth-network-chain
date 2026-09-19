package types

import (
	"context"
)

// PersonhoodKeeper is the chamber's electoral roll.
//
// One method, and it returns a nullifier rather than a weight: this chamber has
// no scale. A live registration votes once and an account without one does not
// vote, so there is no number for this interface to carry.
type PersonhoodKeeper interface {
	// LiveNullifier returns the nullifier of addr's live registration. The bool
	// is false when the account holds no registration, or holds one that has
	// lapsed.
	LiveNullifier(ctx context.Context, addr []byte) ([]byte, bool, error)
}

// The chamber depends on *govkeeper.Keeper concretely rather than through an
// interface of its own. x/gov exposes its proposal queues as collection fields
// rather than as methods, and an interface cannot carry a field — wrapping them
// would mean re-declaring x/gov's storage layout here, which is the one thing
// certain to rot silently across an SDK upgrade. The direction is
// assembly -> gov and nothing points back, so there is no cycle to avoid.

// AllocationKeeper is the one place the chamber can act on its own account
// rather than by refusal: removing a groundworks option.
type AllocationKeeper interface {
	// RemoveGroundworksOption strikes a live groundworks option: its weight is
	// zeroed, its unclaimed balance burned, and the record kept so that voters
	// still naming it are not stranded.
	RemoveGroundworksOption(ctx context.Context, caller []byte, optionID uint64) error
	// GroundworksOptionRemovable reports whether the option exists on the
	// groundworks stream and has not already been removed.
	GroundworksOptionRemovable(ctx context.Context, optionID uint64) (bool, error)
}
