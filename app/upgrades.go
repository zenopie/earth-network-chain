package app

import (
	"context"
	"fmt"

	storetypes "cosmossdk.io/store/types"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

// Upgrade is one named, governance-scheduled chain upgrade.
//
// When a MsgSoftwareUpgrade proposal passes, the chain halts at the plan height
// with `UPGRADE "<name>" NEEDED`. Operators then swap in a binary whose Upgrades
// list contains an entry with a matching Name; that binary migrates state and
// continues. A binary without the matching entry halts again at the same height.
type Upgrade struct {
	// Name must exactly match the name in the passed MsgSoftwareUpgrade plan.
	Name string

	// CreateHandler runs once, at the upgrade height, before any block is
	// processed. Return RunMigrations to execute the registered module
	// migrations; add bespoke state fixes around it as needed.
	CreateHandler func(app *App) upgradetypes.UpgradeHandler

	// StoreUpgrades declares module stores added, renamed or deleted by this
	// upgrade. This is NOT optional when the module set changes: the commit
	// multistore has to be told about a new store key before it can be loaded,
	// and the chain will fail to start otherwise.
	StoreUpgrades storetypes.StoreUpgrades
}

// Upgrades lists every upgrade this binary can perform. Entries are kept after
// they have been applied, so a node syncing from genesis replays each one in
// turn.
//
// To add an upgrade named "v2" that introduces a new module store:
//
//	{
//	    Name: "v2",
//	    CreateHandler: func(app *App) upgradetypes.UpgradeHandler {
//	        return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
//	            return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
//	        }
//	    },
//	    StoreUpgrades: storetypes.StoreUpgrades{Added: []string{"newmodule"}},
//	}
var Upgrades = []Upgrade{
	{
		// The first upgrade this chain performs, and deliberately the smallest
		// one available: dead code removed and documentation corrected. Nothing
		// it changes is consensus state.
		//
		// That is the point. app/upgrades.go was well built and had never run
		// against live validators -- the halt, the store loader, the handler
		// lookup and cosmovisor's download had executed only in
		// scripts/rehearse-cosmovisor.sh. The first time they run for real
		// should not also be the first time the state they migrate matters.
		//
		// RunMigrations is still called rather than skipped: it writes the
		// module version map, so a later upgrade that does need a migration
		// starts from a consistent one.
		Name:          "v0.5.1",
		CreateHandler: defaultUpgradeHandler,
		// Empty on purpose. No module is added, renamed or removed, so there is
		// no store to create -- and declaring one that does not exist is how an
		// upgrade fails at load with a store key nobody can explain.
		StoreUpgrades: storetypes.StoreUpgrades{},
	},
}

// setupUpgrades registers the upgrade handlers and, if the node is restarting
// into a pending upgrade, installs the store loader that applies its
// StoreUpgrades.
//
// MUST be called before app.Load(): the store loader has to be in place before
// the multistore is read, and BaseApp panics if it is set afterwards.
func (app *App) setupUpgrades() {
	for _, u := range Upgrades {
		app.UpgradeKeeper.SetUpgradeHandler(u.Name, u.CreateHandler(app))
	}

	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		panic(fmt.Sprintf("failed to read upgrade info from disk: %v", err))
	}
	// No pending upgrade, or the operator explicitly chose to skip this height.
	if upgradeInfo.Name == "" || app.UpgradeKeeper.IsSkipHeight(upgradeInfo.Height) {
		return
	}

	for _, u := range Upgrades {
		if u.Name != upgradeInfo.Name {
			continue
		}
		// Copy: the loader keeps the pointer past this loop iteration.
		storeUpgrades := u.StoreUpgrades
		app.SetStoreLoader(upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &storeUpgrades))
		return
	}
}

// defaultUpgradeHandler runs the standard module migrations and nothing else.
// Suitable for any upgrade that changes logic or parameters but not the set of
// modules.
//
// Used by v0.4.0 above, and the shape every upgrade starts from.
// scripts/rehearse-upgrade.sh injects exactly this line to exercise the
// halt-and-resume path, so deleting it as dead code breaks that rehearsal.
func defaultUpgradeHandler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}
