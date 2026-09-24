package app

import (
	"context"
	"embed"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

// The verifying keys for the register circuits as recompiled with bounded hash
// binding. Embedded for the reason v070Assets gives: they are consensus, and a
// file an operator could substitute would be a fork waiting to happen.
//
//go:embed upgrades/v091/verifying-keys/*.vk.b64
var v091Assets embed.FS

const v091VerifyingKeyDir = "upgrades/v091/verifying-keys"

// upgradeV091 carries the two criticals from the 2026-09-23 review. See the
// Upgrades entry for what each one is.
//
//  1. The allocation ledger must balance before the fix lands, for the reason
//     assertAllocationLedgerBalances gives: a ledger already short would halt
//     on the first block after the upgrade with the fix apparently to blame.
//  2. The verifying-key swap. Unlike v0.7.0's there is no binary-side format
//     change to keep in step with — the chain's view of a proof is unchanged —
//     but it is done here rather than by a params proposal so the old keys stop
//     verifying at exactly the height the release says, not whenever a second
//     proposal happens to pass.
//  3. Move the assembly's open votes onto ballots. earth-1 has none unless a
//     proposal or removal ballot is open at the upgrade height.
//  4. Module migrations, as usual. None are registered for this release.
func upgradeV091(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		if err := app.AllocationKeeper.AssertInvariants(ctx); err != nil {
			return nil, fmt.Errorf(
				"v0.9.1 refuses to run: the allocation ledger does not balance before the upgrade: %w", err)
		}
		if err := swapVerifyingKeys(ctx, app, v091Assets, v091VerifyingKeyDir, "v0.9.1"); err != nil {
			return nil, err
		}
		// The chamber files votes by ballot from this height. Anything in the
		// v0.9.0 layout moves across, indexed by nullifier as it goes.
		if err := app.AssemblyKeeper.MigrateToBallots(ctx); err != nil {
			return nil, fmt.Errorf("v0.9.1: could not move the assembly's open votes onto ballots: %w", err)
		}
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}
