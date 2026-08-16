package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

func (k Keeper) erthDenom(ctx context.Context) (string, error) {
	return k.dexKeeper.HubDenom(ctx)
}

// payRegistrationReward draws on the human stream's registration-rewards option
// and pays it out on a new registration: 50% registree / 50% referrer (100%
// registree if there is no referrer). Returns the amount paid to the registree.
//
// Only a fixed fraction (RegistrationRewardBps) of the stacked pool is paid, so
// each reward is normalized to the pool size and the pool decays gradually
// rather than being fully drained by whoever happens to register next.
func (k Keeper) payRegistrationReward(ctx context.Context, registree, referrer sdk.AccAddress) (math.Int, error) {
	payout, err := k.allocationKeeper.DrawFromOption(ctx, types.AllocationStream, types.RegistrationRewardOptionID, types.RegistrationRewardBps)
	if err != nil {
		return math.ZeroInt(), err
	}
	if !payout.IsPositive() {
		return math.ZeroInt(), nil
	}

	erth, err := k.erthDenom(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	referrerAmt := math.ZeroInt()
	if referrer != nil {
		referrerAmt = payout.QuoRaw(2)
	}
	registreeAmt := payout.Sub(referrerAmt)

	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, sdk.NewCoins(sdk.NewCoin(erth, payout))); err != nil {
		return math.ZeroInt(), err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, registree, sdk.NewCoins(sdk.NewCoin(erth, registreeAmt))); err != nil {
		return math.ZeroInt(), err
	}
	if referrerAmt.IsPositive() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, referrer, sdk.NewCoins(sdk.NewCoin(erth, referrerAmt))); err != nil {
			return math.ZeroInt(), err
		}
	}
	return registreeAmt, nil
}
