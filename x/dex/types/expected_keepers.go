package types

import (
	"context"

	"cosmossdk.io/core/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
	// Methods imported from account should be defined here
}

// StakingKeeper is a denom oracle, and nothing else. x/dex does not cross
// staking: no pool, swap, auction or reward path reads a delegation, and this
// module has no stake-weighted logic of its own.
//
// It is here because the hub asset every pool pairs against is the staking coin,
// and BondDenom is the only runtime accessor for the chain's denom that does not
// require x/dex to carry a param of its own. That identity is deliberate — ERTH
// is both — but it is an identity the code assumes rather than enforces. If the
// two ever needed to differ, the fix is a hub_denom param on this module, not a
// second denom read from somewhere else.
//
// Keep this interface at one method. It previously declared GetDelegatorBonded,
// GetDelegation and GetValidator, none of which x/dex ever called; they were
// implemented only by the test stub, and the comment above them described
// x/allocation's vote-weight logic, which lives in
// x/allocation/keeper/keeper.go.
type StakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	GetSupply(ctx context.Context, denom string) sdk.Coin
	// GetBalance and GetAllBalances back the balance invariant: what the module
	// actually holds against what its own records say it owes. GetAllBalances is
	// what makes a coin the module holds for no recorded reason visible at all.
	// See keeper/invariants.go.
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}

// BurnRecorder is x/earth's cumulative burn counters, narrowed to the one call
// this module makes into them. Burns are unobservable after the fact — x/bank
// records only the supply that remains — so every burn here is counted as it
// happens. See x/earth/keeper/burns.go.
type BurnRecorder interface {
	RecordBurn(ctx context.Context, source string, coins sdk.Coins) error
}
