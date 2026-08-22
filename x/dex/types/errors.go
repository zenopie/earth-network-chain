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

	// Genesis liquidity auction.
	ErrAuctionUnavailable = errors.Register(ModuleName, 1109, "no liquidity auction is configured")
	ErrAuctionState       = errors.Register(ModuleName, 1110, "liquidity auction is not in the required state")
	ErrAuctionDuration    = errors.Register(ModuleName, 1111, "auction duration must be positive")
	ErrNoBid              = errors.Register(ModuleName, 1112, "no bid found for this address")
	ErrAlreadyClaimed     = errors.Register(ModuleName, 1113, "auction proceeds already claimed")

	// ErrInvariantBroken means the module's own records no longer agree with the
	// coins it holds. Returned from the EndBlocker, so it halts the chain — see
	// keeper/invariants.go for why that is the intended outcome.
	ErrInvariantBroken = errors.Register(ModuleName, 1114, "dex invariant broken")

	// ErrPoolCreationLocked means the genesis liquidity auction has not settled
	// yet. See Keeper.PoolCreationLocked.
	ErrPoolCreationLocked = errors.Register(ModuleName, 1115,
		"pool creation is locked until the genesis liquidity auction settles")
)
