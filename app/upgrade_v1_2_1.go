package app

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// UpgradeV1_2_1 is earth-1's first software upgrade (UPGRADE_PLAN.md items
// 1 and 2): a delegation is bonded in its own block (it no longer waits for
// the epoch end, sharing rewards it did not earn), and Groundworks is voted
// by stake notes in place (a move's exposure voting pending until its window
// closes), Groundworks positions deleted. The stake circuit changed: its
// verifying key is replaced here. (v1.2.0, the same without the pending
// exposure, was proposed and withdrawn: it never ran.)
const UpgradeV1_2_1 = "v1.2.1"

// The v1.2.1 stake verifying key (base64, bb v5.0.0 UltraHonk,
// noir-recursive), written by scripts/privacy-vks.sh. The launch genesis
// sources (networks/genesis) keep the keys earth-1 started with.
//
//go:embed upgrades/v1_2_1/stake.vk.b64
var v1_2_1StakeVK string

func v1_2_1Handler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		vm, err := app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
		if err != nil {
			return vm, err
		}
		params, err := app.ShieldedKeeper.Params.Get(ctx)
		if err != nil {
			return vm, err
		}
		for name, b64 := range map[string]string{shieldedtypes.CircuitStake: v1_2_1StakeVK} {
			vk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
			if err != nil || len(vk) == 0 {
				return vm, fmt.Errorf("v1.2.1: the embedded %s verifying key does not decode", name)
			}
			params.VerifyingKeys[name] = vk
		}
		if err := params.Validate(); err != nil {
			return vm, err
		}
		if err := app.ShieldedKeeper.Params.Set(ctx, params); err != nil {
			return vm, err
		}
		n, err := app.ShieldedStakingKeeper.MigrateV1_2_1(ctx)
		if err != nil {
			return vm, err
		}
		if n > 0 {
			app.Logger().Error("v1.2.1: Groundworks positions were still open and were deleted; their derth left its validators' supply (the backing stays with the remaining holders)", "positions", n)
			sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent("upgrade_positions_deleted",
				sdk.NewAttribute("count", fmt.Sprint(n))))
		}
		return vm, nil
	}
}
