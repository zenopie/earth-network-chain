package dex

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/depinject/appconfig"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
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
	// AllocationKeeper owns the capital emission stream. The LP-rewards option
	// is one of its INTEGRATED options, and the behaviour behind it — how ERTH
	// is spread over pools by volume — belongs here, so this module registers
	// the handler rather than the allocation module importing the dex.
	AllocationKeeper allocationkeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	DexKeeper keeper.Keeper
	Module    appmodule.AppModule
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
	)
	m := NewAppModule(in.Cdc, k, in.AuthKeeper, in.BankKeeper)

	in.AllocationKeeper.RegisterIntegratedHandler(allocationtypes.STREAM_ID_GROUNDWORKS, allocationtypes.HandlerLPRewards,
		func(ctx context.Context, accrued math.Int) (math.Int, error) {
			resolved, err := k.DistributeLPRewards(ctx, accrued)
			if err != nil {
				return math.ZeroInt(), err
			}
			// The ERTH already exists — x/allocation minted it as the stream
			// accrued — so what moves here are coins, not a second issuance.
			// Module-to-module, because this module's account is on the bank's
			// blocked list and an ordinary send would be refused.
			if err := in.AllocationKeeper.PayOutToModule(ctx, types.ModuleName, resolved); err != nil {
				return math.ZeroInt(), err
			}
			return resolved, nil
		})

	return ModuleOutputs{
		DexKeeper: k,
		Module:    m,
	}
}
