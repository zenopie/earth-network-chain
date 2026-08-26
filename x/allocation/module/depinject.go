package allocation

import (
	"cosmossdk.io/core/address"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/depinject/appconfig"
	"github.com/cosmos/cosmos-sdk/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/allocation/keeper"
	"github.com/earth-network/earth/x/allocation/types"
	earthkeeper "github.com/earth-network/earth/x/earth/keeper"
)

var _ depinject.OnePerModuleType = AppModule{}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (AppModule) IsOnePerModuleType() {}

func init() {
	appconfig.Register(
		&types.Module{},
		appconfig.Provide(ProvideModule),
	)
}

type ModuleInputs struct {
	depinject.In

	Config       *types.Module
	StoreService store.KVStoreService
	Cdc          codec.Codec
	AddressCodec address.Codec

	AuthKeeper    types.AuthKeeper
	BankKeeper    types.BankKeeper
	StakingKeeper types.StakingKeeper
	// EarthKeeper owns the chain's tokenomics, and is taken here only for its
	// burn counters: this module destroys supply and x/earth is where the chain
	// records that it happened.
	EarthKeeper earthkeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	AllocationKeeper keeper.Keeper
	Module           appmodule.AppModule
	// Hooks keep capital-stream vote weights in sync with bonded stake.
	Hooks stakingtypes.StakingHooksWrapper
}

// ProvideModule builds the allocation keeper. It deliberately depends on nothing
// but staking, bank and x/earth's burn counters: the modules that own the two
// streams' behaviour (x/personhood's weight source, x/dex's lp_rewards handler)
// register themselves into this keeper from their own wiring, which is what
// keeps the dependency graph a tree rather than a cycle. x/earth imports none of
// the pillar modules, so depending on it does not threaten that.
func ProvideModule(in ModuleInputs) ModuleOutputs {
	// default to governance authority if not provided
	authority := authtypes.NewModuleAddress(types.GovModuleName)
	if in.Config.Authority != "" {
		authority = authtypes.NewModuleAddressOrBech32Address(in.Config.Authority)
	}
	k := keeper.NewKeeper(
		in.StoreService,
		in.Cdc,
		in.AddressCodec,
		authority,
		in.BankKeeper,
		in.StakingKeeper,
		in.EarthKeeper,
	)
	m := NewAppModule(in.Cdc, k, in.AuthKeeper, in.BankKeeper)

	return ModuleOutputs{
		AllocationKeeper: k,
		Module:           m,
		Hooks:            stakingtypes.StakingHooksWrapper{StakingHooks: k.Hooks()},
	}
}
