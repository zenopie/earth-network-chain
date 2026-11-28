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

// UpgradeV1_4_0 adds MsgClaimAnmlDays: a week of unclaimed ANML days in one
// claim, with one claim circuit proof. It installs the claim verifying key
// and sets the claim floor.
const UpgradeV1_4_0 = "v1.4.0"

// The claim circuit's verifying key (base64, bb v5.0.0 UltraHonk,
// noir-recursive), written by scripts/privacy-vks.sh. earth-2's launch
// genesis (networks/genesis) has no claim key.
//
//go:embed upgrades/v1_4_0/claim.vk.b64
var v1_4_0ClaimVK string

func v1_4_0Handler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		vm, err := app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
		if err != nil {
			return vm, err
		}
		params, err := app.ShieldedKeeper.Params.Get(ctx)
		if err != nil {
			return vm, err
		}
		vk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v1_4_0ClaimVK))
		if err != nil || len(vk) == 0 {
			return vm, fmt.Errorf("v1.4.0: the embedded claim verifying key does not decode")
		}
		if params.VerifyingKeys == nil {
			params.VerifyingKeys = map[string][]byte{}
		}
		params.VerifyingKeys[shieldedtypes.CircuitClaim] = vk
		if err := params.Validate(); err != nil {
			return vm, err
		}
		if err := app.ShieldedKeeper.Params.Set(ctx, params); err != nil {
			return vm, err
		}
		// Until now claim nullifiers were pruned after two days: only
		// yesterday's and today's are still held. A run reaching further back
		// would claim days that may have been claimed already, so runs start
		// no earlier than yesterday. Five days on, the window (today - 6)
		// reaches the floor and the floor stops mattering.
		if err := app.PersonhoodKeeper.RaiseClaimFloor(ctx, sdk.UnwrapSDKContext(ctx).BlockTime().Unix()); err != nil {
			return vm, err
		}
		return vm, nil
	}
}
