package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/dex module sentinel errors
var (
	ErrInvalidSigner    = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInvalidAmount    = errors.Register(ModuleName, 1101, "invalid amount")
	ErrSameDenom        = errors.Register(ModuleName, 1102, "the two pool assets must have different denoms")
	ErrPoolNotFound     = errors.Register(ModuleName, 1103, "pool not found")
	ErrInvalidDenom     = errors.Register(ModuleName, 1104, "denom does not belong to the pool")
	ErrInsufficientPool = errors.Register(ModuleName, 1105, "insufficient pool liquidity")
	ErrSlippage         = errors.Register(ModuleName, 1106, "output amount is below the requested minimum")
	ErrZeroShares       = errors.Register(ModuleName, 1107, "computed share amount is zero")
	ErrPoolExists       = errors.Register(ModuleName, 1108, "a pool already exists for this token")
)
