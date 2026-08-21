package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/allocation module sentinel errors
var (
	ErrInvalidSigner  = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrOptionNotFound = errors.Register(ModuleName, 1101, "allocation option not found")
	ErrBadPercentages = errors.Register(ModuleName, 1102, "allocation percentages must be unique and sum to 100")
	ErrUnknownKind    = errors.Register(ModuleName, 1103, "unknown allocation kind")
	ErrNotClaimable   = errors.Register(ModuleName, 1104, "allocation option is not claimable")
	ErrNoWeight       = errors.Register(ModuleName, 1105, "voter carries no weight in this stream")
	ErrUnknownHandler = errors.Register(ModuleName, 1106, "unknown integrated allocation handler")
	ErrUnknownStream  = errors.Register(ModuleName, 1107, "unknown allocation stream")

	// ErrInvariantBroken means a stream's declared total weight no longer equals
	// the sum of its options' allocations. Returned from the EndBlocker, so it
	// halts the chain — see keeper/invariants.go for why.
	ErrInvariantBroken = errors.Register(ModuleName, 1108, "allocation invariant broken")
)
