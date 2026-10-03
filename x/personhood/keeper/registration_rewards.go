package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

func (k Keeper) erthDenom(ctx context.Context) (string, error) {
	return k.dexKeeper.HubDenom(ctx)
}

// rewardNote is where the registrant's share of the registration reward is
// minted: a note to the pc its wallet made, with its ciphertext.
type rewardNote struct {
	pc, ciphertext []byte
}

// referralNote is the referrer's share: a note the chain makes to the
// referrer handle's registered owner_pk with an opening it derives
// (privacy.ReferralOpening), so the registrant names the handle and nothing
// else about where the note goes.
type referralNote struct {
	handle   string
	ownerPK  fr.Element
	rho, rcm fr.Element
}

// referralNoteFor resolves handle and derives the opening of the referral
// note registration (nullifier, leafIndex) mints to it. nil when the handle
// stopped resolving after the ante checked it (released or changed by an
// earlier tx in the block): the registration then lands unreferred, drawing
// the unreferred rate, rather than failing after its fee was paid.
func (k Keeper) referralNoteFor(ctx context.Context, handle string, nullifier []byte, leafIndex uint64) (*referralNote, error) {
	rec, live, err := k.liveHandle(ctx, handle)
	if err != nil || !live {
		return nil, err
	}
	ownerPK, err := privacy.FieldFromBytes(rec.OwnerPk)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrNoReferrer, "affiliate_handle %q: owner_pk: %v", handle, err)
	}
	nf, err := privacy.FieldFromBytes(nullifier)
	if err != nil {
		return nil, err
	}
	rho, rcm := privacy.ReferralOpening(nf, leafIndex)
	return &referralNote{handle: handle, ownerPK: ownerPK, rho: rho, rcm: rcm}, nil
}

// registrationPayout is what payRegistrationReward minted.
type registrationPayout struct {
	registrant, referral math.Int
	referralPosition     uint64
}

// payRegistrationReward draws on the caretaker stream's registration-rewards
// option and mints it into the shielded pool: half as the registrant's note,
// half as the referrer's (referralNote: a note the chain makes to the handle's
// registered address).
//
// With no referrer only the registrant's half is DRAWN, and the other half
// stays in the option's pool rather than being minted: naming a referrer never
// costs the person naming them.
//
// The ERTH already exists in x/allocation's account (a stream mints as its
// index advances); it moves allocation -> this module -> the pool, where
// MintNote counts it into the uerth turnstile. Each half fits a note's u64:
// a draw is RegistrationRewardPpm (1e-4) of the option, and an option would
// need 1.8e23 uerth, orders of magnitude past the supply, for a half to
// reach 2^64; a draw past that fails the registration rather than losing it.
func (k Keeper) payRegistrationReward(ctx context.Context, registrant rewardNote, referrer *referralNote) (registrationPayout, error) {
	out := registrationPayout{registrant: math.ZeroInt(), referral: math.ZeroInt()}
	drawPpm := int64(types.RegistrationRewardPpm)
	if referrer == nil {
		drawPpm = types.RegistrationRewardPpm / 2
	}
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return out, err
	}
	payout, err := k.allocationKeeper.DrawFromOption(ctx, types.AllocationStream, types.RegistrationRewardOptionID, drawPpm)
	if err != nil {
		return out, err
	}
	if !payout.IsPositive() {
		return out, nil
	}
	referrerAmt := math.ZeroInt()
	if referrer != nil {
		referrerAmt = payout.QuoRaw(2)
	}
	registrantAmt := payout.Sub(referrerAmt)

	if err := k.allocationKeeper.PayOutToModule(ctx, types.ModuleName, payout); err != nil {
		return out, err
	}
	denom, err := k.erthDenom(ctx)
	if err != nil {
		return out, err
	}
	if registrantAmt.IsPositive() {
		if _, _, err := k.shieldedKeeper.MintNote(ctx, types.ModuleName, sdk.NewCoin(denom, registrantAmt), registrant.pc, registrant.ciphertext); err != nil {
			return out, err
		}
	}
	out.registrant = registrantAmt
	if referrer != nil && referrerAmt.IsPositive() {
		pos, _, err := k.shieldedKeeper.MintOpenNote(ctx, types.ModuleName, sdk.NewCoin(denom, referrerAmt), referrer.ownerPK, referrer.rho, referrer.rcm)
		if err != nil {
			return out, err
		}
		out.referral, out.referralPosition = referrerAmt, pos
	}
	return out, nil
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
