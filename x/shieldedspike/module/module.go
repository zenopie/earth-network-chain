package module

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	gwruntime "github.com/grpc-ecosystem/grpc-gateway/runtime"

	"github.com/earth-network/earth/x/shieldedspike/keeper"
	"github.com/earth-network/earth/x/shieldedspike/types"
)

var (
	_ module.AppModuleBasic = AppModule{}
	_ module.HasServices    = AppModule{}
)

// AppModule is registered manually (app.RegisterModules), like IBC and wasm.
type AppModule struct{ k keeper.Keeper }

func NewAppModule(k keeper.Keeper) AppModule { return AppModule{k: k} }

func (AppModule) IsOnePerModuleType() {}
func (AppModule) IsAppModule()        {}
func (AppModule) Name() string        { return types.ModuleName }

func (AppModule) RegisterLegacyAminoCodec(*codec.LegacyAmino) {}
func (AppModule) RegisterInterfaces(r codectypes.InterfaceRegistry) {
	types.RegisterInterfaces(r)
}
func (AppModule) RegisterGRPCGatewayRoutes(client.Context, *gwruntime.ServeMux) {}

func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.k))
}
