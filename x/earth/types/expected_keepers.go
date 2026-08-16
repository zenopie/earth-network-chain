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

// BankKeeper is the bank surface tokenomics needs: minting the emission, moving
// it into the pool that backs the stake it is added to, and burning gas fees.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// StakingKeeper is the staking surface the emission needs.
//
// Adding tokens to a validator without issuing shares is not something the SDK
// exposes as a single call — RemoveValidatorTokens is the only token-only
// mutator, and it exists for slashing — so power-index maintenance is done here
// by hand, in the same sequence, reversed.
type StakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	TotalBondedTokens(ctx context.Context) (math.Int, error)
	GetBondedValidatorsByPower(ctx context.Context) ([]stakingtypes.Validator, error)
	GetValidator(ctx context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error)
	DeleteValidatorByPowerIndex(ctx context.Context, validator stakingtypes.Validator) error
	SetValidator(ctx context.Context, validator stakingtypes.Validator) error
	SetValidatorByPowerIndex(ctx context.Context, validator stakingtypes.Validator) error
	// Delegate compounds withheld commission into the earning validator's own
	// self-delegation. It fires the staking hooks, which is how the operator's
	// allocation weight comes back in step — and the operator pays gas for that
	// resync, since they submit the message.
	Delegate(
		ctx context.Context,
		delAddr sdk.AccAddress,
		bondAmt math.Int,
		tokenSrc stakingtypes.BondStatus,
		validator stakingtypes.Validator,
		subtractAccount bool,
	) (math.LegacyDec, error)
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}
