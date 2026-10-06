package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// WeightSource answers the only two questions that differ between the streams:
// how much weight a voter carries, and whether they may vote at all.
//
// Everything else — options, the index maths, the epoch reset, the claim, the
// INTEGRATED/ADDRESS split — is shared. Zero weight means "not eligible": the
// human stream returns zero for an address with no live registration, the
// capital stream for an address with no bonded stake.
type WeightSource interface {
	Weight(ctx context.Context, addr []byte) (math.Int, error)
}

// Lapser retires leased weight in a stream at the exact time it lapses.
// Lapse runs only from x/allocation's BeginBlock sweep (SweepLapses), never
// from a tx-time settle. The stream's index is settled up to each lapse
// time before Lapse runs, and Lapse writes voters without settling further
// (SetWeightedVoterSettled), so the emission after a lapse is never shared
// with the lapsed weight. x/shieldedstaking's stake positions are one.
type Lapser interface {
	// NextLapse is the earliest pending lapse time (unix seconds) at or
	// before t, if any.
	NextLapse(ctx context.Context, t int64) (int64, bool, error)
	// Lapse retires everything lapsing at exactly t. It must leave no entry
	// at t behind (one it cannot retire it re-files later), so the next
	// NextLapse moves on.
	Lapse(ctx context.Context, t int64) error
}

// BondedTracker is implemented by a weight source whose account voters weigh
// their bonded stake only for some keys. The staking hooks resync a voter
// from its bonded stake only when TracksBonded(key) holds; a source without
// it (the default bonded-stake source) tracks every account.
type BondedTracker interface {
	TracksBonded(key []byte) bool
}

// StakingKeeper defines the expected interface for the Staking module. It
// resolves the hub denom (the staking coin, ERTH) and a delegator's bonded
// stake, which is their weight in the capital stream.
type StakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	GetDelegatorBonded(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
	GetValidator(ctx context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error)
	IterateDelegatorDelegations(ctx context.Context, delegator sdk.AccAddress, cb func(delegation stakingtypes.Delegation) (stop bool)) error
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	GetSupply(ctx context.Context, denom string) sdk.Coin
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// CommunityPoolKeeper is the SDK's community pool, satisfied by x/distribution's
// keeper. It is deliberately the funding call and nothing else.
//
// FundCommunityPool does two things that must happen together: it moves the
// coins into the distribution module account and it adds them to FeePool, which
// is what governance actually spends from. A bank transfer alone would leave the
// coins in the account and invisible to the pool, so they could never be paid
// out again.
type CommunityPoolKeeper interface {
	FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error
}

// BurnRecorder is x/earth's cumulative burn counters, narrowed to the one call
// this module makes into them. Burns are unobservable after the fact — x/bank
// records only the supply that remains — so every burn here is counted as it
// happens. See x/earth/keeper/burns.go.
type BurnRecorder interface {
	RecordBurn(ctx context.Context, source string, coins sdk.Coins) error
}
