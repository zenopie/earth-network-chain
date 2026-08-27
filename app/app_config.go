package app

import (
	"time"

	runtimev1alpha1 "cosmossdk.io/api/cosmos/app/runtime/v1alpha1"
	appv1alpha1 "cosmossdk.io/api/cosmos/app/v1alpha1"
	authmodulev1 "cosmossdk.io/api/cosmos/auth/module/v1"
	authzmodulev1 "cosmossdk.io/api/cosmos/authz/module/v1"
	bankmodulev1 "cosmossdk.io/api/cosmos/bank/module/v1"
	circuitmodulev1 "cosmossdk.io/api/cosmos/circuit/module/v1"
	consensusmodulev1 "cosmossdk.io/api/cosmos/consensus/module/v1"
	distrmodulev1 "cosmossdk.io/api/cosmos/distribution/module/v1"
	epochsmodulev1 "cosmossdk.io/api/cosmos/epochs/module/v1"
	evidencemodulev1 "cosmossdk.io/api/cosmos/evidence/module/v1"
	feegrantmodulev1 "cosmossdk.io/api/cosmos/feegrant/module/v1"
	genutilmodulev1 "cosmossdk.io/api/cosmos/genutil/module/v1"
	govmodulev1 "cosmossdk.io/api/cosmos/gov/module/v1"
	groupmodulev1 "cosmossdk.io/api/cosmos/group/module/v1"
	mintmodulev1 "cosmossdk.io/api/cosmos/mint/module/v1"
	nftmodulev1 "cosmossdk.io/api/cosmos/nft/module/v1"
	paramsmodulev1 "cosmossdk.io/api/cosmos/params/module/v1"
	slashingmodulev1 "cosmossdk.io/api/cosmos/slashing/module/v1"
	stakingmodulev1 "cosmossdk.io/api/cosmos/staking/module/v1"
	txconfigv1 "cosmossdk.io/api/cosmos/tx/config/v1"
	upgrademodulev1 "cosmossdk.io/api/cosmos/upgrade/module/v1"
	vestingmodulev1 "cosmossdk.io/api/cosmos/vesting/module/v1"
	"cosmossdk.io/depinject/appconfig"
	_ "cosmossdk.io/x/circuit" // import for side-effects
	circuittypes "cosmossdk.io/x/circuit/types"
	_ "cosmossdk.io/x/evidence" // import for side-effects
	evidencetypes "cosmossdk.io/x/evidence/types"
	"cosmossdk.io/x/feegrant"
	_ "cosmossdk.io/x/feegrant/module" // import for side-effects
	"cosmossdk.io/x/nft"
	_ "cosmossdk.io/x/nft/module" // import for side-effects
	_ "cosmossdk.io/x/upgrade"    // import for side-effects
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	_ "github.com/cosmos/cosmos-sdk/x/auth/tx/config" // import for side-effects
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	_ "github.com/cosmos/cosmos-sdk/x/auth/vesting" // import for side-effects
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	_ "github.com/cosmos/cosmos-sdk/x/authz/module" // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/bank"         // import for side-effects
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	_ "github.com/cosmos/cosmos-sdk/x/consensus" // import for side-effects
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	_ "github.com/cosmos/cosmos-sdk/x/distribution" // import for side-effects
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	_ "github.com/cosmos/cosmos-sdk/x/epochs" // import for side-effects
	epochstypes "github.com/cosmos/cosmos-sdk/x/epochs/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	_ "github.com/cosmos/cosmos-sdk/x/gov" // import for side-effects
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	_ "github.com/cosmos/cosmos-sdk/x/group/module" // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/mint"         // import for side-effects
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	_ "github.com/cosmos/cosmos-sdk/x/params" // import for side-effects
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	_ "github.com/cosmos/cosmos-sdk/x/slashing" // import for side-effects
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	_ "github.com/cosmos/cosmos-sdk/x/staking" // import for side-effects
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	icatypes "github.com/cosmos/ibc-go/v10/modules/apps/27-interchain-accounts/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	_ "github.com/earth-network/earth/x/allocation/module"
	allocationmoduletypes "github.com/earth-network/earth/x/allocation/types"
	_ "github.com/earth-network/earth/x/dex/module"
	dexmoduletypes "github.com/earth-network/earth/x/dex/types"
	_ "github.com/earth-network/earth/x/earth/module"
	earthmoduletypes "github.com/earth-network/earth/x/earth/types"
	_ "github.com/earth-network/earth/x/personhood/module"
	personhoodmoduletypes "github.com/earth-network/earth/x/personhood/types"
	_ "github.com/earth-network/earth/x/pki/module"
	pkimoduletypes "github.com/earth-network/earth/x/pki/types"
	"google.golang.org/protobuf/types/known/durationpb"
)

var (
	moduleAccPerms = []*authmodulev1.ModuleAccountPermission{
		{Account: authtypes.FeeCollectorName},
		{Account: distrtypes.ModuleName},
		{Account: minttypes.ModuleName, Permissions: []string{authtypes.Minter}},
		{Account: stakingtypes.BondedPoolName, Permissions: []string{authtypes.Burner, stakingtypes.ModuleName}},
		{Account: stakingtypes.NotBondedPoolName, Permissions: []string{authtypes.Burner, stakingtypes.ModuleName}},
		{Account: govtypes.ModuleName, Permissions: []string{authtypes.Burner}},
		{Account: nft.ModuleName},
		{Account: ibctransfertypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner}},
		{Account: icatypes.ModuleName},
		{Account: dexmoduletypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner, authtypes.Staking}},
		// x/allocation mints an option's accrued ERTH when it is claimed and burns
		// the fee for adding one. x/personhood mints ANML and the registration
		// reward, and burns the ANML it buys back.
		{Account: allocationmoduletypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner}},
		{Account: personhoodmoduletypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner, authtypes.Staking}},
		// x/earth owns issuance: it mints the emission on its way to the fee
		// collector, and burns gas fees. Without these permissions MintCoins panics.
		// It needs no Staking permission — it never touches the staking pools.
		{Account: earthmoduletypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner}},
		// x/wasm burns, and only burns. The permission serves CosmWasm's
		// BankMsg::Burn: wasmd moves the burning contract's coins into this
		// module account and destroys them there, so a contract's own balance
		// does pass through it, on any denom a contract can hold. That path is
		// counted into x/earth by the burnRecorder decorator in app/wasm.go.
		//
		// A second, far rarer path uses the same permission: instantiating a
		// contract at an address already holding a vesting account prunes that
		// account and burns its original vesting balances (wasmd's
		// VestingCoinBurner, the default AccountPruner). That one is NOT
		// counted — see burnRecorder's comment.
		//
		// It never mints. A contract address is an ordinary account, not a
		// module account, so nothing a contract merely holds is here.
		{Account: wasmtypes.ModuleName, Permissions: []string{authtypes.Burner}}}

	// Blocked account addresses — MsgSend and MsgMultiSend refuse them as a
	// recipient. Setting this list at all overrides the SDK default, which is to
	// block every account holding module permissions, so anything omitted here is
	// deliberately *reachable* by an ordinary transfer.
	//
	// The chain's own module accounts are blocked because sending to one is
	// always a mistake: nothing credits the sender, and there is no path that
	// pays the coins back out. On x/dex that mistake would also be undetectable
	// — the module holds the whole pre-mine in one balance, and its solvency
	// invariant (x/dex/keeper/invariants.go) is an exact equality between what it
	// records and what it holds. An outside deposit would either break the chain
	// or force the invariant to be weakened to "at least", which stops catching
	// coins that should have been burned and were not.
	//
	// This does NOT leave keeper-level moves untouched, which an earlier version
	// of this comment claimed and which cost the ANML buyback every trade it ever
	// tried to make. SendCoinsFromModuleToAccount consults BlockedAddr too, so
	// paying a blocked module account fails with "not allowed to receive funds"
	// wherever the call is made from. A module trading against the dex has to go
	// through SendCoinsFromModuleToModule, which does not consult the list — see
	// swapParty in x/dex/keeper/msg_server_swap.go.
	blockAccAddrs = []string{
		authtypes.FeeCollectorName,
		distrtypes.ModuleName,
		minttypes.ModuleName,
		stakingtypes.BondedPoolName,
		stakingtypes.NotBondedPoolName,
		nft.ModuleName,
		dexmoduletypes.ModuleName,
		earthmoduletypes.ModuleName,
		allocationmoduletypes.ModuleName,
		personhoodmoduletypes.ModuleName,
		// Blocked for the usual reason: nothing credits a sender who pays it and
		// there is no path back out. Contracts are unaffected — they hold coins
		// at their own ordinary addresses, not here — and x/wasm's own burn path
		// goes through SendCoinsFromAccountToModule, which does not consult this
		// list.
		wasmtypes.ModuleName,
		// We allow the following module accounts to receive funds:
		// govtypes.ModuleName
	}

	// application configuration (used by depinject)
	appConfig = appconfig.Compose(&appv1alpha1.Config{
		Modules: []*appv1alpha1.ModuleConfig{
			{
				Name: runtime.ModuleName,
				Config: appconfig.WrapAny(&runtimev1alpha1.Module{
					AppName: Name,
					// NOTE: upgrade module is required to be prioritized
					PreBlockers: []string{
						upgradetypes.ModuleName,
						authtypes.ModuleName,
						// this line is used by starport scaffolding # stargate/app/preBlockers
					},
					// During begin block slashing happens after distr.BeginBlocker so that
					// there is nothing left over in the validator fee pool, so as to keep the
					// CanWithdrawInvariant invariant.
					// NOTE: staking module is required if HistoricalEntries param > 0
					BeginBlockers: []string{
						minttypes.ModuleName,
						distrtypes.ModuleName,
						slashingtypes.ModuleName,
						evidencetypes.ModuleName,
						stakingtypes.ModuleName,
						authz.ModuleName,
						epochstypes.ModuleName,
						// ibc modules
						ibcexported.ModuleName,
						// chain modules
						earthmoduletypes.ModuleName,
						dexmoduletypes.ModuleName,
						// personhood before allocation: its sweep returns a lapsed human's
						// weight to the human stream, and that has to happen before the
						// stream settles this block's emission across the voters left.
						personhoodmoduletypes.ModuleName,
						allocationmoduletypes.ModuleName,
						// this line is used by starport scaffolding # stargate/app/beginBlockers
					},
					EndBlockers: []string{
						govtypes.ModuleName,
						stakingtypes.ModuleName,
						feegrant.ModuleName,
						group.ModuleName,
						// chain modules
						earthmoduletypes.ModuleName,
						dexmoduletypes.ModuleName,
						personhoodmoduletypes.ModuleName,
						// allocation last: its EndBlocker only verifies that each stream's
						// declared weight still matches its options, so it should see every
						// other module's writes for the block before it does.
						allocationmoduletypes.ModuleName,
						// this line is used by starport scaffolding # stargate/app/endBlockers
					},
					// The following is mostly only needed when ModuleName != StoreKey name.
					OverrideStoreKeys: []*runtimev1alpha1.StoreKeyConfig{
						{
							ModuleName: authtypes.ModuleName,
							KvStoreKey: "acc",
						},
					},
					// NOTE: The genutils module must occur after staking so that pools are
					// properly initialized with tokens from genesis accounts.
					// NOTE: The genutils module must also occur after auth so that it can access the params from auth.
					InitGenesis: []string{
						consensustypes.ModuleName,
						authtypes.ModuleName,
						banktypes.ModuleName,
						distrtypes.ModuleName,
						stakingtypes.ModuleName,
						slashingtypes.ModuleName,
						govtypes.ModuleName,
						minttypes.ModuleName,
						genutiltypes.ModuleName,
						evidencetypes.ModuleName,
						authz.ModuleName,
						feegrant.ModuleName,
						vestingtypes.ModuleName,
						nft.ModuleName,
						group.ModuleName,
						upgradetypes.ModuleName,
						circuittypes.ModuleName,
						epochstypes.ModuleName,
						// ibc modules
						ibcexported.ModuleName,
						ibctransfertypes.ModuleName,
						icatypes.ModuleName,
						// chain modules
						earthmoduletypes.ModuleName,
						dexmoduletypes.ModuleName,
						allocationmoduletypes.ModuleName,
						personhoodmoduletypes.ModuleName,
						pkimoduletypes.ModuleName,
						// wasm last: a contract in genesis may call any other
						// module, so every module it could reach has to have
						// initialised first.
						wasmtypes.ModuleName,
						// this line is used by starport scaffolding # stargate/app/initGenesis
					},
				}),
			},
			{
				Name: authtypes.ModuleName,
				Config: appconfig.WrapAny(&authmodulev1.Module{
					Bech32Prefix:                AccountAddressPrefix,
					ModuleAccountPermissions:    moduleAccPerms,
					EnableUnorderedTransactions: true,
					// By default modules authority is the governance module. This is configurable with the following:
					// Authority: "group", // A custom module authority can be set using a module name
					// Authority: "cosmos1cwwv22j5ca08ggdv9c2uky355k908694z577tv", // or a specific address
				}),
			},
			{
				Name:   vestingtypes.ModuleName,
				Config: appconfig.WrapAny(&vestingmodulev1.Module{}),
			},
			{
				Name: banktypes.ModuleName,
				Config: appconfig.WrapAny(&bankmodulev1.Module{
					BlockedModuleAccountsOverride: blockAccAddrs,
				}),
			},
			{
				Name:   stakingtypes.ModuleName,
				Config: appconfig.WrapAny(&stakingmodulev1.Module{}),
			},
			{
				Name:   slashingtypes.ModuleName,
				Config: appconfig.WrapAny(&slashingmodulev1.Module{}),
			},
			{
				Name:   "tx",
				Config: appconfig.WrapAny(&txconfigv1.Config{}),
			},
			{
				Name:   genutiltypes.ModuleName,
				Config: appconfig.WrapAny(&genutilmodulev1.Module{}),
			},
			{
				Name:   authz.ModuleName,
				Config: appconfig.WrapAny(&authzmodulev1.Module{}),
			},
			{
				Name:   upgradetypes.ModuleName,
				Config: appconfig.WrapAny(&upgrademodulev1.Module{}),
			},
			{
				Name:   distrtypes.ModuleName,
				Config: appconfig.WrapAny(&distrmodulev1.Module{}),
			},
			{
				Name:   evidencetypes.ModuleName,
				Config: appconfig.WrapAny(&evidencemodulev1.Module{}),
			},
			{
				Name:   minttypes.ModuleName,
				Config: appconfig.WrapAny(&mintmodulev1.Module{}),
			},
			{
				Name: group.ModuleName,
				Config: appconfig.WrapAny(&groupmodulev1.Module{
					MaxExecutionPeriod: durationpb.New(time.Second * 1209600),
					MaxMetadataLen:     255,
				}),
			},
			{
				Name:   nft.ModuleName,
				Config: appconfig.WrapAny(&nftmodulev1.Module{}),
			},
			{
				Name:   feegrant.ModuleName,
				Config: appconfig.WrapAny(&feegrantmodulev1.Module{}),
			},
			{
				Name:   govtypes.ModuleName,
				Config: appconfig.WrapAny(&govmodulev1.Module{}),
			},
			{
				Name:   consensustypes.ModuleName,
				Config: appconfig.WrapAny(&consensusmodulev1.Module{}),
			},
			{
				Name:   circuittypes.ModuleName,
				Config: appconfig.WrapAny(&circuitmodulev1.Module{}),
			},
			{
				Name:   paramstypes.ModuleName,
				Config: appconfig.WrapAny(&paramsmodulev1.Module{}),
			},
			{
				Name:   epochstypes.ModuleName,
				Config: appconfig.WrapAny(&epochsmodulev1.Module{}),
			},
			{
				Name:   earthmoduletypes.ModuleName,
				Config: appconfig.WrapAny(&earthmoduletypes.Module{}),
			},
			{
				Name:   dexmoduletypes.ModuleName,
				Config: appconfig.WrapAny(&dexmoduletypes.Module{}),
			},
			{
				Name:   allocationmoduletypes.ModuleName,
				Config: appconfig.WrapAny(&allocationmoduletypes.Module{}),
			},
			{
				Name:   personhoodmoduletypes.ModuleName,
				Config: appconfig.WrapAny(&personhoodmoduletypes.Module{}),
			},
			{
				Name:   pkimoduletypes.ModuleName,
				Config: appconfig.WrapAny(&pkimoduletypes.Module{}),
			},
			// this line is used by starport scaffolding # stargate/app/moduleConfig
		},
	})
)
