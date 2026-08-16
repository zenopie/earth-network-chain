package types

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
}

// StakingKeeper defines the expected interface for the Staking module. It is
// used to resolve the hub denom (the staking coin, ERTH) and to read a
// delegator's bonded stake, which is their voting weight in the allocation
// stream.
type StakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	GetDelegatorBonded(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
	GetDelegation(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (stakingtypes.Delegation, error)
	GetValidator(ctx context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error)
	// Delegate is used to compound withheld commission into the earning
	// validator's own self-delegation. It fires the staking hooks, which is how
	// the operator's allocation weight is brought back in step — and the operator
	// pays gas for that resync, since they are the one submitting the message.
	Delegate(
		ctx context.Context,
		delAddr sdk.AccAddress,
		bondAmt math.Int,
		tokenSrc stakingtypes.BondStatus,
		validator stakingtypes.Validator,
		subtractAccount bool,
	) (math.LegacyDec, error)
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	GetSupply(ctx context.Context, denom string) sdk.Coin
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// EarthKeeper is the tokenomics surface this module needs. Stake weights are
// stored normalized by the stake compounding index, which x/earth owns, so that
// a voter who touches their delegation is not marked to market ahead of one who
// has not voted since the stake compounded.
type EarthKeeper interface {
	NormalizeStakeWeight(ctx context.Context, weight math.Int) (math.Int, error)
}

// DexKeeper defines the expected interface for the x/dex module. The
// stake-weighted LP-rewards allocation option distributes its accrued ERTH into
// the dex pools (weighted by trading volume); the dex owns that logic.
type DexKeeper interface {
	DistributeLPRewards(ctx context.Context, amount math.Int) (math.Int, error)
}
