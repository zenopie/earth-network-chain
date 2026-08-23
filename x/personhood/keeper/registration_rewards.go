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
// Only a fixed fraction (RegistrationRewardPpm) of the stacked pool is paid, so
// each reward is normalized to the pool size and the pool decays gradually
// rather than being fully drained by whoever happens to register next.
func (k Keeper) payRegistrationReward(ctx context.Context, registree, referrer sdk.AccAddress) (math.Int, error) {
	// Halving the draw, rather than drawing in full and returning the remainder,
	// is what keeps the unmatched half in the pool: the option has no deposit
	// path, so anything drawn cannot be put back.
	//
	// The rate is in parts per million precisely so this halving is exact. In
	// basis points it was integer division on a single-digit number, one step
	// away from truncating to zero and paying an unreferred registrant nothing.
	drawPpm := int64(types.RegistrationRewardPpm)
	if referrer == nil {
		drawPpm = types.RegistrationRewardPpm / 2
	}

	payout, err := k.allocationKeeper.DrawFromOption(ctx, types.AllocationStream, types.RegistrationRewardOptionID, drawPpm)
	if err != nil {
		return math.ZeroInt(), err
	}
	if !payout.IsPositive() {
		return math.ZeroInt(), nil
	}

	referrerAmt := math.ZeroInt()
	if referrer != nil {
		referrerAmt = payout.QuoRaw(2)
	}
	registreeAmt := payout.Sub(referrerAmt)

	// Paid out of the allocation module account, not minted here. x/allocation
	// issues a stream's emission when the index advances, so by the time a
	// registration draws on the pool the ERTH already exists and this module has
	// no business creating any.
	if err := k.allocationKeeper.PayOut(ctx, registree, registreeAmt); err != nil {
		return math.ZeroInt(), err
	}
	if referrerAmt.IsPositive() {
		if err := k.allocationKeeper.PayOut(ctx, referrer, referrerAmt); err != nil {
			return math.ZeroInt(), err
		}
	}
	return registreeAmt, nil
}
