package app

import (
	"fmt"

	"cosmossdk.io/core/appmodule"
	storetypes "cosmossdk.io/store/types"
	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cast"

	allocationmoduletypes "github.com/earth-network/earth/x/allocation/types"
	dexmoduletypes "github.com/earth-network/earth/x/dex/types"
	earthmoduletypes "github.com/earth-network/earth/x/earth/types"
	personhoodmoduletypes "github.com/earth-network/earth/x/personhood/types"
	pkimoduletypes "github.com/earth-network/earth/x/pki/types"
)

// x/wasm on earth is permissionless: anyone may upload code and anyone may
// instantiate it, paying only gas. That is wasmd's own default
// (wasmtypes.DefaultParams), so the genesis in networks/genesis/app_state.json
// states it explicitly rather than inheriting it — a chain that is open by
// accident and a chain that is open on purpose look identical until someone
// changes the default underneath you.
//
// Two consequences worth holding in mind, neither of them a reason to close it:
//
//   - Code blobs are up to 800KB (wasmtypes.MaxWasmSize) and live in state
//     forever. Gas is the only thing pricing that, and x/earth burns half of
//     every fee rather than paying it to the validators actually storing the
//     bytes. If upload spam ever becomes real, CodeUploadAccess is a governance
//     parameter and can be tightened without a binary upgrade.
//   - A contract address is an ordinary account. It is not a module account, so
//     it is not on blockAccAddrs, and x/dex's solvency invariant is unaffected
//     by anything a contract holds.

// wasmAcceptedQueries is the allowlist of gRPC queries a contract may call.
//
// wasmd rejects every gRPC and Stargate query by default, and that default is
// the right one: a contract that reads a protobuf response is frozen to that
// response's wire shape, so every path listed here is a promise that the
// response type will not change incompatibly for as long as the path is
// allowed. Adding a field is fine; renumbering, removing or repurposing one
// breaks deployed contracts silently, which is far worse than breaking them
// loudly.
//
// Messages need no equivalent list. Contracts dispatch them as
// CosmosMsg::Any, which wasmd routes through the same MsgServiceRouter a
// normal transaction uses (wasmkeeper.EncodeAnyMsg), so every module's own
// validation, authority checks and events apply unchanged. There is nothing a
// contract can send that a user could not have sent themselves.
//
// The list is deliberately short and made of bounded, keyed lookups. Paginated
// list queries — dex ListPool, allocation Options, personhood
// RegistrationCountries — are left out for now: they are deterministic, but
// they let one contract call walk an unbounded amount of state, and nothing has
// asked for them yet. Adding a path later is a one-line change plus an upgrade;
// removing one after contracts depend on it is not.
func wasmAcceptedQueries() wasmkeeper.AcceptedQueries {
	return wasmkeeper.AcceptedQueries{
		// x/personhood — the reason contracts on this chain are interesting at
		// all. Registration answers "is this address a live verified human",
		// which is what a sybil-resistant airdrop, a one-human-one-vote poll or
		// a human-gated game needs and cannot get anywhere else.
		"/earth.personhood.v1.Query/Registration": func() proto.Message {
			return &personhoodmoduletypes.QueryRegistrationResponse{}
		},
		"/earth.personhood.v1.Query/RegistrationCount": func() proto.Message {
			return &personhoodmoduletypes.QueryRegistrationCountResponse{}
		},
		"/earth.personhood.v1.Query/Params": func() proto.Message {
			return &personhoodmoduletypes.QueryParamsResponse{}
		},

		// x/dex — pool reserves, so a contract can price a swap before sending
		// MsgSwap as an Any message.
		"/earth.dex.v1.Query/GetPool": func() proto.Message {
			return &dexmoduletypes.QueryGetPoolResponse{}
		},
		"/earth.dex.v1.Query/LiquidityAuction": func() proto.Message {
			return &dexmoduletypes.QueryLiquidityAuctionResponse{}
		},
		"/earth.dex.v1.Query/Params": func() proto.Message {
			return &dexmoduletypes.QueryParamsResponse{}
		},

		// x/allocation — a contract that wants to be an allocation option, or
		// to report what a voter has committed, reads it here.
		"/earth.allocation.v1.Query/Option": func() proto.Message {
			return &allocationmoduletypes.QueryOptionResponse{}
		},
		"/earth.allocation.v1.Query/Voter": func() proto.Message {
			return &allocationmoduletypes.QueryVoterResponse{}
		},
		"/earth.allocation.v1.Query/Params": func() proto.Message {
			return &allocationmoduletypes.QueryParamsResponse{}
		},

		// x/earth and x/pki params: emission rate and trust-store settings.
		// Params responses are the safest thing on this list — they are a single
		// keyed read and the type is stable by construction.
		"/earth.earth.v1.Query/Params": func() proto.Message {
			return &earthmoduletypes.QueryParamsResponse{}
		},
		"/earth.pki.v1.Query/Params": func() proto.Message {
			return &pkimoduletypes.QueryParamsResponse{}
		},

		// SDK modules. Bank and staking already have native CosmWasm query
		// variants (BankQuery, StakingQuery) that need no allowlist; these are
		// the ones those variants do not cover.
		"/cosmos.auth.v1beta1.Query/Account": func() proto.Message {
			return &authtypes.QueryAccountResponse{}
		},
		"/cosmos.bank.v1beta1.Query/DenomMetadata": func() proto.Message {
			return &banktypes.QueryDenomMetadataResponse{}
		},
		"/cosmos.staking.v1beta1.Query/Delegation": func() proto.Message {
			return &stakingtypes.QueryDelegationResponse{}
		},
		"/cosmos.staking.v1beta1.Query/UnbondingDelegation": func() proto.Message {
			return &stakingtypes.QueryUnbondingDelegationResponse{}
		},
		"/cosmos.staking.v1beta1.Query/Validator": func() proto.Message {
			return &stakingtypes.QueryValidatorResponse{}
		},
		"/cosmos.distribution.v1beta1.Query/DelegationRewards": func() proto.Message {
			return &distrtypes.QueryDelegationRewardsResponse{}
		},
	}
}

// registerWasmKeeper mounts the x/wasm store and builds its keeper.
//
// Called from registerIBCModules rather than standing on its own, because it
// sits in the middle of that wiring: the keeper needs the channel and transfer
// keepers to give contracts IBC, and the IBC routers in turn need the wasm
// handler to deliver packets addressed to a contract. Splitting it into a
// separate pass would mean building the keeper before the routers and the
// routes after, in two places, for no gain.
func (app *App) registerWasmKeeper(appOpts servertypes.AppOptions) error {
	if err := app.RegisterStores(storetypes.NewKVStoreKey(wasmtypes.StoreKey)); err != nil {
		return err
	}

	// Node-local settings — smart query gas limit, memory cache size, contract
	// debug logging. Not consensus: two nodes may differ here without forking,
	// which is why it comes from app.toml and not from genesis. See
	// cmd/earthd/cmd/config.go for the template that writes the section.
	nodeConfig, err := wasm.ReadNodeConfig(appOpts)
	if err != nil {
		return fmt.Errorf("reading wasm node config: %w", err)
	}
	app.WasmNodeConfig = nodeConfig

	govModuleAddr, err := app.AuthKeeper.AddressCodec().BytesToString(authtypes.NewModuleAddress(govtypes.ModuleName))
	if err != nil {
		return err
	}

	// homeDir, not homeDir/wasm: the keeper appends "wasm" itself when it opens
	// the wasmvm cache. Passing the joined path lands the compiled-module cache
	// in ~/.earth/wasm/wasm, which works but is a confusing thing to find.
	homeDir := cast.ToString(appOpts.Get(flags.FlagHome))

	app.WasmKeeper = wasmkeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(wasmtypes.StoreKey)),
		app.AuthKeeper,
		app.BankKeeper,
		app.StakingKeeper,
		distrkeeper.NewQuerier(app.DistrKeeper),
		app.IBCKeeper.ChannelKeeper, // ICS4Wrapper
		app.IBCKeeper.ChannelKeeper,
		app.IBCKeeper.ChannelKeeperV2,
		app.TransferKeeper,
		app.MsgServiceRouter(),
		app.GRPCQueryRouter(),
		homeDir,
		nodeConfig,
		wasmtypes.VMConfig{},
		wasmkeeper.BuiltInCapabilities(),
		govModuleAddr,
		// Open the query door to the allowlist above. Without this option the
		// keeper keeps DefaultQueryPlugins' Reject*Querier and contracts cannot
		// see any chain module — including x/personhood.
		//
		// Both doors, one list. Grpc is what cosmwasm-std 2.x contracts use and
		// answers in protobuf; Stargate is the 1.x spelling and answers in JSON.
		// They are the same queries with the same allowlist, so serving only the
		// modern one would reject perfectly good contracts for the version of a
		// Rust crate they were built against — while the chain advertises the
		// "stargate" capability and accepts them at upload time regardless.
		//
		// The option merges into DefaultQueryPlugins rather than replacing it,
		// so the native Bank, Staking, Distribution, IBC and Wasm queriers are
		// untouched.
		wasmkeeper.WithQueryPlugins(&wasmkeeper.QueryPlugins{
			Grpc:     wasmkeeper.AcceptListGrpcQuerier(wasmAcceptedQueries(), app.GRPCQueryRouter(), app.appCodec),
			Stargate: wasmkeeper.AcceptListStargateQuerier(wasmAcceptedQueries(), app.GRPCQueryRouter(), app.appCodec),
		}),
	)

	return nil
}

// registerWasmModule adds x/wasm to the module manager. Separate from the
// keeper because it has to run after the IBC routers are set, and because
// RegisterModules is what wires genesis, queries and msg services.
func (app *App) registerWasmModule() error {
	return app.RegisterModules(
		wasm.NewAppModule(
			app.appCodec,
			&app.WasmKeeper,
			app.StakingKeeper,
			app.AuthKeeper,
			app.BankKeeper,
			app.MsgServiceRouter(),
			nil, // no legacy param subspace: this chain has never had one to migrate
		),
	)
}

// registerWasmSnapshotter teaches state sync about contract code.
//
// Contract bytecode is stored outside the IAVL tree, so a node restored from a
// snapshot without this extension comes up with every contract's metadata and
// none of its code, and halts the first time one is called. Must run before
// app.Load — the snapshot manager is built by baseapp and sealed at load.
func (app *App) registerWasmSnapshotter() error {
	manager := app.SnapshotManager()
	if manager == nil {
		// No snapshot store configured (in-memory apps and most tests).
		return nil
	}
	return manager.RegisterExtensions(
		wasmkeeper.NewWasmSnapshotter(app.CommitMultiStore(), &app.WasmKeeper),
	)
}

// initializeWasmPinnedCodes reloads pinned contracts into the wasmvm cache.
//
// Pinning lives in consensus state but the cache it populates is node-local and
// empty on every restart, so it has to be replayed from state after the store
// is loaded. Skipped when the app was built without loading a version — there
// is no state to read yet.
func (app *App) initializeWasmPinnedCodes(loadLatest bool) error {
	if !loadLatest {
		return nil
	}
	ctx := app.NewUncachedContext(true, cmtproto.Header{})
	return app.WasmKeeper.InitializePinnedCodes(ctx)
}

// RegisterWasm mirrors RegisterIBC: x/wasm is wired by hand rather than by
// depinject, so the client side never learns about it from app.AppConfig() and
// has to be told separately.
//
// Without this, three things are quietly missing and none of them fail loudly:
// `earthd init` writes a genesis with no "wasm" section, so the chain starts
// with no wasm params at all; `earthd tx wasm store` does not exist; and
// MsgStoreCode has no interface registration, so a client cannot even decode
// one. The keeper is a zero value on purpose — only AppModuleBasic's genesis,
// codec and CLI methods are reached from here, and none of them touch it.
func RegisterWasm(cdc codec.Codec) map[string]appmodule.AppModule {
	modules := map[string]appmodule.AppModule{
		wasmtypes.ModuleName: wasm.NewAppModule(cdc, &wasmkeeper.Keeper{}, nil, nil, nil, nil, nil),
	}

	for _, m := range modules {
		if mr, ok := m.(module.AppModuleBasic); ok {
			mr.RegisterInterfaces(cdc.InterfaceRegistry())
		}
	}

	return modules
}
