package types

import (
	"context"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// PersonhoodKeeper is the chamber's electoral roll: the identity tree a voter
// proves membership of, anonymously. The chamber never learns who voted, only
// that a live registration did, and under which per-ballot nullifier.
type PersonhoodKeeper interface {
	// CheckMembership refuses a proof whose root is not a current identity
	// anchor.
	CheckMembership(ctx context.Context, m personhoodtypes.Membership) error
	// VerifyMembership verifies the proof against the statement.
	VerifyMembership(ctx context.Context, m personhoodtypes.Membership, st personhoodtypes.MembershipStatement) error
	// MembershipActionGas prices a membership proof and `writes` note-sized
	// writes.
	MembershipActionGas(ctx context.Context, writes uint64) (uint64, error)
	// SignalOf is a private msg's sighash on this chain, which its membership
	// proof binds as its signal.
	SignalOf(ctx context.Context, msg shieldedtypes.PrivateMsg) (fr.Element, error)
	// IdentityRootWindow is how long a superseded identity root stays an
	// anchor, which every activation bound subtracts.
	IdentityRootWindow(ctx context.Context) (int64, error)
}

// PkiKeeper places a revocation in a country (see keeper/subjects.go).
type PkiKeeper interface {
	// DscIssuerCountry is the issuing country of the registrations under a
	// DSC certificate: "" when unknown, placed false when no CSCA can have
	// issued it.
	DscIssuerCountry(ctx context.Context, der []byte) (country string, placed bool, err error)
	// CscaKeyCountry is the country of the trust-store certificates carrying
	// a CSCA certificate's key, "" when unknown.
	CscaKeyCountry(ctx context.Context, der []byte) (string, error)
}

// ShieldedKeeper runs the chamber's private msgs through the private ante.
type ShieldedKeeper interface {
	RegisterPrivateAction(msgTypeURL string, h shieldedtypes.PrivateActionHandler)
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
