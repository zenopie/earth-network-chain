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

// rewardNote is where one share of the registration reward is minted.
type rewardNote struct {
	pc, ciphertext []byte
}

// payRegistrationReward draws on the caretaker stream's registration-rewards
// option: half minted into the shielded pool as the registrant's note, half
// paid in transparent ERTH to the referrer's address (referrals are public).
// Returns the registrant's amount.
//
// With no referrer only the registrant's half is DRAWN, and the other half
// stays in the option's pool rather than being minted: naming a referrer never
// costs the person naming them.
//
// The ERTH already exists in x/allocation's account (a stream mints as its
// index advances); the registrant's half moves allocation -> this module ->
// the pool, where MintNote counts it into the uerth turnstile, and the
// referrer's goes allocation -> referrer.
func (k Keeper) payRegistrationReward(ctx context.Context, registrant rewardNote, referrer sdk.AccAddress) (math.Int, error) {
	drawPpm := int64(types.RegistrationRewardPpm)
	if referrer == nil {
		drawPpm = types.RegistrationRewardPpm / 2
	}
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return math.ZeroInt(), err
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
	registrantAmt := payout.Sub(referrerAmt)

	if err := k.allocationKeeper.PayOut(ctx, referrer, referrerAmt); err != nil {
		return math.ZeroInt(), err
	}
	if err := k.allocationKeeper.PayOutToModule(ctx, types.ModuleName, registrantAmt); err != nil {
		return math.ZeroInt(), err
	}
	denom, err := k.erthDenom(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	if registrantAmt.IsPositive() {
		if _, _, err := k.shieldedKeeper.MintNote(ctx, types.ModuleName, sdk.NewCoin(denom, registrantAmt), registrant.pc, registrant.ciphertext); err != nil {
			return math.ZeroInt(), err
		}
	}
	return registrantAmt, nil
}

// mintAnmlNote mints one ANML and deposits it as a note to pc.
func (k Keeper) mintAnmlNote(ctx context.Context, pc, ciphertext []byte) (uint64, error) {
	anml := sdk.NewInt64Coin(types.AnmlDenom, types.OneAnml)
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, sdk.NewCoins(anml)); err != nil {
		return 0, err
	}
	pos, _, err := k.shieldedKeeper.MintNote(ctx, types.ModuleName, anml, pc, ciphertext)
	return pos, err
}
