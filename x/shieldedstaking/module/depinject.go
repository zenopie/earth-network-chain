package shieldedstaking

import (
	"cosmossdk.io/core/address"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/depinject/appconfig"
	"github.com/cosmos/cosmos-sdk/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	"github.com/earth-network/earth/x/shieldedstaking/keeper"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

var _ depinject.OnePerModuleType = AppModule{}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (AppModule) IsOnePerModuleType() {}

func init() {
	appconfig.Register(
		&types.Module{},
		appconfig.Provide(ProvideModule,
			types.ProvideDelegateGetSigners, types.ProvideRestakeGetSigners, types.ProvideUndelegateGetSigners,
			types.ProvideStakeVoteGetSigners, types.ProvideLockPositionGetSigners, types.ProvideUpdatePositionGetSigners,
			types.ProvideUnlockPositionGetSigners, types.ProvidePositionVoteGetSigners),
	)
}

type ModuleInputs struct {
	depinject.In

	Config       *types.Module
	StoreService store.KVStoreService
	Cdc          codec.Codec
	AddressCodec address.Codec

	AuthKeeper       types.AuthKeeper
	BankKeeper       types.BankKeeper
	StakingKeeper    types.StakingKeeper
	DistrKeeper      types.DistrKeeper
	SlashingKeeper   types.SlashingKeeper
	ShieldedKeeper   shieldedkeeper.Keeper
	AllocationKeeper allocationkeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	ShieldedStakingKeeper keeper.Keeper
	Module                appmodule.AppModule
	// Tally is x/gov's custom tally: transparent votes plus stake votes.
	Tally govkeeper.CalculateVoteResultsAndVotingPowerFn
	// GovHooks snapshot proposals entering voting.
	GovHooks govtypes.GovHooksWrapper
	// StakingHooks refuse transparent delegation and pass slashes through to
	// pending undelegations.
	StakingHooks stakingtypes.StakingHooksWrapper
}

// ProvideModule builds the keeper and wires it into the modules it extends:
// its private msgs' actions and its shielded-only denoms into x/shielded, the
// Groundworks weight source into x/allocation. x/gov's keeper is bound later
// (keeper.SetGovKeeper, from app.New): x/gov takes this module's tally, so it
// cannot also be an input here.
func ProvideModule(in ModuleInputs) ModuleOutputs {
	authority := authtypes.NewModuleAddress(types.GovModuleName)
	if in.Config.Authority != "" {
		authority = authtypes.NewModuleAddressOrBech32Address(in.Config.Authority)
	}
	k := keeper.NewKeeper(in.StoreService, in.Cdc, in.AddressCodec, authority,
		in.AuthKeeper, in.BankKeeper, in.StakingKeeper, in.DistrKeeper, in.SlashingKeeper,
		in.ShieldedKeeper, in.AllocationKeeper)

	keeper.RegisterPrivateActions(in.ShieldedKeeper.RegisterPrivateAction, keeper.NewActionHandler(k))
	// derth lives in this module's stake note tree: never a shielded-pool
	// asset, never a coin an account or the dex may hold (shielded-only, so
	// every transparent path refuses it by name).
	in.ShieldedKeeper.ExcludeAssetPrefix(types.DerthPrefix)
	in.ShieldedKeeper.RegisterShieldedOnlyPrefix(types.DerthPrefix, types.ModuleName)
	// Groundworks weight is a position's derth x rate; an account's bonded
	// stake no longer says anything (this module is the only delegator).
	in.AllocationKeeper.RegisterWeightSource(allocationtypes.STREAM_ID_GROUNDWORKS, keeper.NewPositionWeightSource(k))

	return ModuleOutputs{
		ShieldedStakingKeeper: k,
		Module:                NewAppModule(in.Cdc, k),
		Tally:                 k.StakeTally(),
		GovHooks:              govtypes.GovHooksWrapper{GovHooks: k.GovHooks()},
		StakingHooks:          stakingtypes.StakingHooksWrapper{StakingHooks: k.StakingHooks()},
	}
}
