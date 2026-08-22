package personhood

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
	dexkeeper "github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
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

	AuthKeeper types.AuthKeeper
	BankKeeper types.BankKeeper
	DexKeeper  dexkeeper.Keeper
	PkiKeeper  pkikeeper.Keeper
	// AllocationKeeper owns the human emission stream. The dependency runs one
	// way — this module registers itself into the allocation keeper below rather
	// than being reached for — which is what keeps the two out of a cycle.
	AllocationKeeper allocationkeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	PersonhoodKeeper keeper.Keeper
	Module           appmodule.AppModule
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
		in.DexKeeper,
		in.PkiKeeper,
		in.AllocationKeeper,
	)
	m := NewAppModule(in.Cdc, k, in.AuthKeeper, in.BankKeeper)

	// Teach the human stream who may vote and with how much weight. Only this
	// module can answer that — it is the one holding the registrations.
	in.AllocationKeeper.RegisterWeightSource(types.AllocationStream, k)
	// Revoking a Document Signer starts retiring the registrations made under it.
	in.PkiKeeper.RegisterRevocationListener(k)

	// The registration-rewards pool resolves nothing per block: it stacks, and is
	// drawn down when a human registers (see payRegistrationReward). Registering
	// the handler anyway is what lets the genesis option's handler name resolve
	// and lets governance add further options backed by the same behaviour.
	in.AllocationKeeper.RegisterIntegratedHandler(types.AllocationStream, types.HandlerRegistrationRewards,
		func(context.Context, math.Int) (math.Int, error) {
			return math.ZeroInt(), nil
		})

	return ModuleOutputs{PersonhoodKeeper: k, Module: m}
}
