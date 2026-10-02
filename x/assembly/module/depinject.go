package assembly

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

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	"github.com/earth-network/earth/x/assembly/keeper"
	"github.com/earth-network/earth/x/assembly/types"
	personhoodkeeper "github.com/earth-network/earth/x/personhood/keeper"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
)

var _ depinject.OnePerModuleType = AppModule{}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (AppModule) IsOnePerModuleType() {}

func init() {
	appconfig.Register(
		&types.Module{},
		appconfig.Provide(ProvideModule,
			types.ProvideVoteProposalGetSigners, types.ProvideProposeRemovalGetSigners, types.ProvideVoteRemovalGetSigners),
	)
}

type ModuleInputs struct {
	depinject.In

	Config       *types.Module
	StoreService store.KVStoreService
	Cdc          codec.Codec
	AddressCodec address.Codec

	GovKeeper        *govkeeper.Keeper
	PersonhoodKeeper personhoodkeeper.Keeper
	AllocationKeeper allocationkeeper.Keeper
	ShieldedKeeper   shieldedkeeper.Keeper
	PkiKeeper        pkikeeper.Keeper
}

type ModuleOutputs struct {
	depinject.Out

	AssemblyKeeper keeper.Keeper
	Module         appmodule.AppModule
	// GovHooks fix each proposal's subjects as it enters voting.
	GovHooks govtypes.GovHooksWrapper
}

// ProvideModule builds the assembly keeper.
//
// Note what is not here: no authority. Every other module in this chain takes
// one and defaults it to x/gov, because governance is what configures them. This
// chamber is what checks governance, so there is nothing here for governance to
// set — its thresholds are constants in types/keys.go and there is no params
// message to change them with.
//
// It registers itself into x/allocation rather than being handed to it, which is
// the same direction x/personhood's weight source and x/dex's reward handler
// already travel. That is what keeps the module graph a tree: assembly knows
// about allocation, allocation knows nothing about assembly.
func ProvideModule(in ModuleInputs) ModuleOutputs {
	chamberAddr := authtypes.NewModuleAddress(types.ModuleName)

	k := keeper.NewKeeper(
		in.StoreService,
		in.Cdc,
		in.AddressCodec,
		chamberAddr,
		in.PersonhoodKeeper,
		in.GovKeeper,
		allocationkeeper.NewChamberFacade(in.AllocationKeeper),
		in.PkiKeeper,
	)
	in.AllocationKeeper.RegisterChamber(chamberAddr)
	// Every msg of the chamber is private: its fee bundle and membership
	// proof are checked in the private ante.
	k.RegisterPrivateActions(in.ShieldedKeeper)

	return ModuleOutputs{
		AssemblyKeeper: k,
		Module:         NewAppModule(in.Cdc, k),
		GovHooks:       govtypes.GovHooksWrapper{GovHooks: k.GovHooks()},
	}
}
