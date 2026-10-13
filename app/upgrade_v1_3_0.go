package app

import (
	"context"
	"strings"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

// UpgradeV1_3_0 introduces monthly stake vintages (UPGRADE_PLAN.md item 4):
// a stake note's asset names the month its stake was last checked in, and
// a vintage not checked in for 12 to 13 months retires (its value booked as
// a held undelegation its notes withdraw from); Groundworks stake votes end
// with their vintage instead of a lease. No circuit or verifying key
// changes: every existing note's unsuffixed derth/<valoper> asset is the
// upgrade month's vintage.
//
// The migration never fails the upgrade (a handler error halts the chain):
// a step that fails is logged and evented and left as it was.
const UpgradeV1_3_0 = "v1.3.0"

func v1_3_0Handler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		vm, err := app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
		if err != nil {
			return vm, err
		}
		rep := app.ShieldedStakingKeeper.MigrateV1_3_0(ctx)
		if len(rep.Failures) > 0 {
			app.Logger().Error("v1.3.0: stake vintage migration steps failed; the chain goes on, fix by a follow-up upgrade",
				"failures", strings.Join(rep.Failures, "; "))
		}
		app.Logger().Info("v1.3.0: stake vintages", "base_vintage", rep.BaseVintage, "books", rep.Books,
			"votes", rep.Votes, "moves", rep.Moves)
		return vm, nil
	}
}
