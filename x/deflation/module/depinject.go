package deflation

import (
	"cosmossdk.io/core/address"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/depinject/appconfig"
	"github.com/cosmos/cosmos-sdk/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	earthkeeper "github.com/earth-network/earth/x/earth/keeper"
	dexkeeper "github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/deflation/keeper"
	"github.com/earth-network/earth/x/deflation/types"
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
	DexKeeper     dexkeeper.Keeper
	// EarthKeeper supplies the stake compounding index that vote weights are
	// normalized by, so compounding does not silently reshuffle voting power.
	EarthKeeper earthkeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	DeflationKeeper keeper.Keeper
	Module          appmodule.AppModule
	// Hooks registers the deflation staking hooks that keep allocation vote
	// weights in sync with bonded stake.
	Hooks stakingtypes.StakingHooksWrapper
}

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
		in.DexKeeper,
		in.EarthKeeper,
	)
	m := NewAppModule(in.Cdc, k, in.AuthKeeper, in.BankKeeper)

	return ModuleOutputs{
		DeflationKeeper: k,
		Module:          m,
		Hooks:           stakingtypes.StakingHooksWrapper{StakingHooks: k.Hooks()},
	}
}
