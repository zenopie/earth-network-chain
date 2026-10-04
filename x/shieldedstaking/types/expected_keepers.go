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
	// GetAccount is for refusing a vesting account as a validator operator.
	GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins
	SpendableCoins(ctx context.Context, addr sdk.AccAddress) sdk.Coins
	// SpendableCoin guards the epoch's compounding: it must not change an
	// operator's spendable balance.
	SpendableCoin(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetSupply(ctx context.Context, denom string) sdk.Coin
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
	// SendCoins moves a reward escrow's balance to its operator.
	SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error
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
	// GetBondedValidatorsByPower is the active set: the validators whose
	// operators' self-bond rewards compound at each epoch end.
	GetBondedValidatorsByPower(ctx context.Context) ([]stakingtypes.Validator, error)
	// GetAllValidators is every validator, for the genesis check that no
	// operator's withdraw address points elsewhere.
	GetAllValidators(ctx context.Context) ([]stakingtypes.Validator, error)
	// UnbondingTime and MaxEntries bound the module's unbonding entries
	// per validator (Params.MaxEpochUnbondings) and time an operator's
	// retirement (escrow.go).
	UnbondingTime(ctx context.Context) (time.Duration, error)
	MaxEntries(ctx context.Context) (uint32, error)
	// The genesis check of the delegation rule (only this module and each
	// operator's self-bond delegate; only this module redelegates): x/staking
	// loads an exported genesis's delegations without running the hooks.
	GetAllDelegations(ctx context.Context) ([]stakingtypes.Delegation, error)
	IterateRedelegations(ctx context.Context, fn func(index int64, red stakingtypes.Redelegation) (stop bool)) error
	IterateUnbondingDelegations(ctx context.Context, fn func(index int64, ubd stakingtypes.UnbondingDelegation) (stop bool)) error
	// Private redelegation (MsgRedelegate): the module moves its own stake
	// between validators with x/staking's primitives (no transitive lock, no
	// max_entries: its stake is pooled, and each move's slash exposure is
	// carried by its notes) and records the redelegation entry itself, so a
	// slash of the source still reaches it (GetRedelegationsFromSrcValidator).
	Unbond(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, shares math.LegacyDec) (math.Int, error)
	SetRedelegationEntry(ctx context.Context, delegatorAddr sdk.AccAddress, validatorSrcAddr, validatorDstAddr sdk.ValAddress,
		creationHeight int64, minTime time.Time, balance math.Int, sharesSrc, sharesDst math.LegacyDec) (stakingtypes.Redelegation, error)
	SetRedelegation(ctx context.Context, red stakingtypes.Redelegation) error
	DeleteUnbondingIndex(ctx context.Context, id uint64) error
	InsertRedelegationQueue(ctx context.Context, red stakingtypes.Redelegation, completionTime time.Time) error
	GetRedelegation(ctx context.Context, delAddr sdk.AccAddress, valSrcAddr, valDstAddr sdk.ValAddress) (stakingtypes.Redelegation, error)
	GetRedelegationsFromSrcValidator(ctx context.Context, valAddr sdk.ValAddress) ([]stakingtypes.Redelegation, error)
	// A slash of a redelegation's source takes its share from the
	// destination: the module's unbonding delegation there is set aside
	// for the slash and put back after it (redelegate.go).
	SetUnbondingDelegation(ctx context.Context, ubd stakingtypes.UnbondingDelegation) error
	RemoveUnbondingDelegation(ctx context.Context, ubd stakingtypes.UnbondingDelegation) error
}

// DistrKeeper is x/distribution: rewards and the community pool.
type DistrKeeper interface {
	WithdrawDelegationRewards(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (sdk.Coins, error)
	// WithdrawValidatorCommission pays a validator's commission to its
	// operator's withdraw address (its reward escrow): the epoch end
	// compounds it into the self-bond.
	WithdrawValidatorCommission(ctx context.Context, valAddr sdk.ValAddress) (sdk.Coins, error)
	IncrementValidatorPeriod(ctx context.Context, val stakingtypes.ValidatorI) (uint64, error)
	CalculateDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI, endingPeriod uint64) (sdk.DecCoins, error)
	FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error
	GetDelegatorWithdrawAddr(ctx context.Context, delAddr sdk.AccAddress) (sdk.AccAddress, error)
	// GetWithdrawAddrEnabled is x/distribution's withdraw_addr_enabled.
	GetWithdrawAddrEnabled(ctx context.Context) (bool, error)
	// DeleteDelegatorWithdrawAddr resets delAddr's withdraw address to
	// itself (the default when none is stored).
	DeleteDelegatorWithdrawAddr(ctx context.Context, delAddr, withdrawAddr sdk.AccAddress) error
	// SetDelegatorWithdrawAddr is the store setter: it bypasses
	// withdraw_addr_enabled, for pointing an operator at its reward escrow.
	SetDelegatorWithdrawAddr(ctx context.Context, delAddr, withdrawAddr sdk.AccAddress) error
}

// SlashingKeeper reports tombstoning, and the two slash fractions x/staking
// is ever called with (x/slashing's downtime, x/evidence's double sign):
// which entries a slash reached is replayed with them (keeper moves.go).
type SlashingKeeper interface {
	IsTombstoned(ctx context.Context, consAddr sdk.ConsAddress) bool
	SlashFractionDoubleSign(ctx context.Context) (math.LegacyDec, error)
	SlashFractionDowntime(ctx context.Context) (math.LegacyDec, error)
}
