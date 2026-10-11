package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

// UpgradeV1_2_2 fixes the stored epoch rate (UPGRADE_PLAN.md item 3): an
// epoch end counted the queue it had just bonded twice, over-weighting
// Groundworks votes. No state migration: the first epoch end after the
// upgrade stores a correct rate and the votes re-weigh at it.
const UpgradeV1_2_2 = "v1.2.2"

func v1_2_2Handler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}
