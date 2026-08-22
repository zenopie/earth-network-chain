package types

import (
	"context"

	"cosmossdk.io/core/address"
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

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
}

// StakingKeeper defines the expected interface for the Staking module. It
// resolves the hub denom (the staking coin, ERTH) and a delegator's bonded
// stake, which is their weight in the capital stream.
type StakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	GetDelegatorBonded(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
	GetDelegation(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (stakingtypes.Delegation, error)
	GetValidator(ctx context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error)
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
