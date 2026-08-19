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

// payRegistrationReward draws on the stream's registration-rewards option and
// pays it out on a new registration: half to the registree, half to the
// referrer. Returns the amount paid to the registree.
//
// With no referrer, only the registree's half is DRAWN — the other half stays
// in the option's accrued pool rather than being minted. The registree is paid
// the same amount either way, which is the whole point: naming a referrer must
// never cost the person naming them. Paying the unmatched half to the registree
// instead (the previous behaviour) made being referred halve your own reward,
// so the rational move was to never name anyone.
//
// Only a fixed fraction (RegistrationRewardBps) of the stacked pool is paid, so
// each reward is normalized to the pool size and the pool decays gradually
// rather than being fully drained by whoever happens to register next.
func (k Keeper) payRegistrationReward(ctx context.Context, registree, referrer sdk.AccAddress) (math.Int, error) {
	// Halving the draw, rather than drawing in full and returning the remainder,
	// is what keeps the unmatched half in the pool: the option has no deposit
	// path, so anything drawn cannot be put back.
	drawBps := int64(types.RegistrationRewardBps)
	if referrer == nil {
		drawBps = types.RegistrationRewardBps / 2
	}

	payout, err := k.allocationKeeper.DrawFromOption(ctx, types.AllocationStream, types.RegistrationRewardOptionID, drawBps)
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
