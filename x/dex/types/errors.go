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

	// ErrLpShareDenom means someone tried to make an LP share coin the spoke
	// side of a pool. See the guard in CreatePool for why that halts the chain.
	ErrLpShareDenom = errors.Register(ModuleName, 1116,
		"an lp share denom cannot be a pool asset")

	// ErrInvalidUnbonding means a queued LP withdrawal is malformed — a missing
	// pool, nil or non-positive shares, or shares of the wrong pool.
	//
	// It cannot be produced by MsgRemoveLiquidity, which validates all three
	// before queueing. It exists for entries that arrive through genesis import,
	// and it is deliberately an error rather than a panic: SweepMaturedUnbondings
	// reports the payout failure and retries the entry later instead of halting
	// the chain on it.
	ErrInvalidUnbonding = errors.Register(ModuleName, 1117,
		"malformed lp unbonding entry")

	// ErrShieldedOnly means a transparent account would send or receive a
	// shielded-only denom (ANML): it exists only as notes, and reaches the
	// dex only through the note paths (MsgNoteSwap, MsgBuyAnml,
	// MsgAddLiquidityShielded, a withdrawal paid as a note).
	ErrShieldedOnly = errors.Register(ModuleName, 1118,
		"denom exists only in the shielded pool")

	// ErrInvalidPrivateMsg is a malformed private dex msg.
	ErrInvalidPrivateMsg = errors.Register(ModuleName, 1119,
		"invalid private dex msg")

	// ErrPoolCap means an operation would take a pool reserve, an LP share
	// supply or an auction's raise past MaxPoolAmount (2^120), past which
	// the share and swap arithmetic could overflow math.Int's 256 bits.
	ErrPoolCap = errors.Register(ModuleName, 1120,
		"amount exceeds the pool cap")
)
