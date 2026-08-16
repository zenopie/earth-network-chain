package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/deflation module sentinel errors
var (
	ErrInvalidSigner  = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrOptionNotFound = errors.Register(ModuleName, 1109, "allocation option not found")
	ErrBadPercentages = errors.Register(ModuleName, 1110, "allocation percentages must be unique and sum to 100")
	ErrUnknownKind    = errors.Register(ModuleName, 1111, "unknown allocation kind")
	ErrNotClaimable   = errors.Register(ModuleName, 1112, "allocation option is not claimable")
	ErrNoStake        = errors.Register(ModuleName, 1113, "voter has no bonded stake")
	ErrUnknownHandler = errors.Register(ModuleName, 1114, "unknown integrated allocation handler")
)
