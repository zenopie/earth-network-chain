package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/earth module sentinel errors
var (
	ErrInvalidSigner = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	// ErrNoCommission covers both "nothing accrued" and "nowhere to put it".
	ErrNoCommission = errors.Register(ModuleName, 1101, "no commission to compound")
)
