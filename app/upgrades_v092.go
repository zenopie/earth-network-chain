package app

import (
	"context"
	"embed"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
)

// The register circuits' verifying keys with the nullifier widened to the
// issuing state and the whole document number. Embedded for the reason
// v070Assets gives.
//
//go:embed upgrades/v092/verifying-keys/*.vk.b64
var v092Assets embed.FS

const v092VerifyingKeyDir = "upgrades/v092/verifying-keys"

// icaAllowMessages is what an interchain account on earth-1 may do from v0.9.2.
// It was "*", which let a controller chain run any message an account can
// sign — including ones this chain added later without thinking about ICA.
// Listed instead: moving, staking and trading tokens, voting with stake,
// directing the capital stream, and calling contracts. Registration and the
// assembly are left out; they are for a person holding their own keys.
var icaAllowMessages = []string{
	"/cosmos.bank.v1beta1.MsgSend",
	"/cosmos.bank.v1beta1.MsgMultiSend",
	"/cosmos.staking.v1beta1.MsgDelegate",
	"/cosmos.staking.v1beta1.MsgUndelegate",
	"/cosmos.staking.v1beta1.MsgBeginRedelegate",
	"/cosmos.staking.v1beta1.MsgCancelUnbondingDelegation",
	"/cosmos.distribution.v1beta1.MsgWithdrawDelegatorReward",
	"/cosmos.distribution.v1beta1.MsgSetWithdrawAddress",
	"/cosmos.gov.v1.MsgVote",
	"/cosmos.gov.v1.MsgVoteWeighted",
	"/cosmos.gov.v1.MsgDeposit",
	"/ibc.applications.transfer.v1.MsgTransfer",
	"/earth.dex.v1.MsgSwap",
	"/earth.dex.v1.MsgAddLiquidity",
	"/earth.dex.v1.MsgRemoveLiquidity",
	"/earth.allocation.v1.MsgSetAllocations",
	"/earth.allocation.v1.MsgClaimAllocation",
	"/cosmwasm.wasm.v1.MsgExecuteContract",
}

// upgradeV092 carries the rest of the 2026-09-23 review.
//
//  1. The allocation ledger must balance first, as in v0.9.1.
//  2. The verifying keys for the widened nullifier.
//  3. Every registration retired, since the old nullifiers cannot be matched
//     against new proofs. earth-1 had one.
//  4. x/dex's LP unbondings indexed by address, from what is already queued.
//  5. Personhood params: proof and DSC verification gas raised, and the new
//     network-wide daily registration cap set.
//  6. Interchain accounts limited to icaAllowMessages.
//  7. Module migrations. None are registered for this release.
func upgradeV092(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		if err := app.AllocationKeeper.AssertInvariants(ctx); err != nil {
			return nil, fmt.Errorf(
				"v0.9.2 refuses to run: the allocation ledger does not balance before the upgrade: %w", err)
		}
		if err := swapVerifyingKeys(ctx, app, v092Assets, v092VerifyingKeyDir, "v0.9.2"); err != nil {
			return nil, err
		}
		if _, err := app.PersonhoodKeeper.RetireAllRegistrations(ctx); err != nil {
			return nil, fmt.Errorf("v0.9.2: could not retire registrations: %w", err)
		}
		if err := app.DexKeeper.IndexLpUnbondingsByAddr(ctx); err != nil {
			return nil, fmt.Errorf("v0.9.2: could not index LP unbondings: %w", err)
		}

		params, err := app.PersonhoodKeeper.Params.Get(ctx)
		if err != nil {
			return nil, err
		}
		params.ProofVerificationGas = personhoodtypes.DefaultProofVerificationGas
		params.DscVerificationGas = personhoodtypes.DefaultDscVerificationGas
		params.NetworkDailyRegistrationFloor = personhoodtypes.DefaultNetworkDailyRegistrationFloor
		params.NetworkDailyRegistrationGrowthBps = personhoodtypes.DefaultNetworkDailyRegistrationGrowthBps
		if err := params.Validate(); err != nil {
			return nil, fmt.Errorf("v0.9.2: personhood params: %w", err)
		}
		if err := app.PersonhoodKeeper.Params.Set(ctx, params); err != nil {
			return nil, err
		}

		sdkCtx := sdk.UnwrapSDKContext(ctx)
		ica := app.ICAHostKeeper.GetParams(sdkCtx)
		ica.AllowMessages = icaAllowMessages
		if err := ica.Validate(); err != nil {
			return nil, fmt.Errorf("v0.9.2: ica host params: %w", err)
		}
		app.ICAHostKeeper.SetParams(sdkCtx, ica)

		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}
