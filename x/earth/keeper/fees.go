package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/earth/types"
)

// BurnCollectedFees burns the transaction fees that accumulated in the fee
// collector during this block, making gas deflationary instead of paying it to
// validators.
//
// Timing: the fixed ERTH staking emission is minted and handed to the
// distribution module during BeginBlock (before any tx in the block executes),
// so by EndBlock the fee collector holds *only* this block's tx fees. Moving
// them into the dex module account and burning them therefore removes exactly
// the gas fees from supply without touching staking rewards.
func (k Keeper) BurnCollectedFees(ctx context.Context) error {
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	fees := k.bankKeeper.SpendableCoins(ctx, feeCollector)
	if fees.IsZero() {
		return nil
	}
	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, authtypes.FeeCollectorName, types.ModuleName, fees); err != nil {
		return err
	}
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, fees); err != nil {
		return err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent("burn_gas_fees", sdk.NewAttribute("amount", fees.String())),
	)
	return nil
}
