package types

import (
	"context"
	"time"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetModuleAddress(moduleName string) sdk.AccAddress
	GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins
	GetSupply(ctx context.Context, denom string) sdk.Coin
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
}

// StakingKeeper is x/staking, driven by this module as a delegator.
type StakingKeeper interface {
	ValidatorAddressCodec() address.Codec
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	GetDelegation(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (stakingtypes.Delegation, error)
	GetUnbondingDelegation(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (stakingtypes.UnbondingDelegation, error)
	Delegate(ctx context.Context, delAddr sdk.AccAddress, bondAmt math.Int, tokenSrc stakingtypes.BondStatus,
		validator stakingtypes.Validator, subtractAccount bool) (math.LegacyDec, error)
	Undelegate(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, sharesAmount math.LegacyDec) (time.Time, math.Int, error)
	ValidateUnbondAmount(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, amt math.Int) (math.LegacyDec, error)
	IterateDelegations(ctx context.Context, delegator sdk.AccAddress, fn func(index int64, delegation stakingtypes.DelegationI) (stop bool)) error
	// GetDelegatorBonded is a validator operator's transparent self-bond (no
	// other account can delegate): its Groundworks weight.
	GetDelegatorBonded(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
	// GetBondedValidatorsByPower is the active set: the validators whose
	// operators' self-bond rewards compound at each epoch end.
	GetBondedValidatorsByPower(ctx context.Context) ([]stakingtypes.Validator, error)
	// GetAllValidators is every validator, for the genesis check that no
	// operator's withdraw address points elsewhere.
	GetAllValidators(ctx context.Context) ([]stakingtypes.Validator, error)
}

// DistrKeeper is x/distribution: rewards and the community pool.
type DistrKeeper interface {
	WithdrawDelegationRewards(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (sdk.Coins, error)
	IncrementValidatorPeriod(ctx context.Context, val stakingtypes.ValidatorI) (uint64, error)
	CalculateDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI, endingPeriod uint64) (sdk.DecCoins, error)
	FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error
	GetDelegatorWithdrawAddr(ctx context.Context, delAddr sdk.AccAddress) (sdk.AccAddress, error)
	// DeleteDelegatorWithdrawAddr resets delAddr's withdraw address to
	// itself (the default when none is stored).
	DeleteDelegatorWithdrawAddr(ctx context.Context, delAddr, withdrawAddr sdk.AccAddress) error
}

// SlashingKeeper reports tombstoning.
type SlashingKeeper interface {
	IsTombstoned(ctx context.Context, consAddr sdk.ConsAddress) bool
}
