package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// The automatic brake on a compromised Document Signer.
//
// A stolen signing key mints valid passport proofs without limit. Everything
// else guarding registration is a check on whether a proof is genuine, and
// against a stolen key every one of those checks passes — the proofs really are
// genuine, they just are not attached to people. The only remaining question is
// how many of them the chain will accept before somebody notices, and until
// there is a cap the answer is "as many as the attacker cares to send".
//
// So this bounds the rate rather than judging the proof. It cannot tell a
// compromise from a popular rollout, and it does not try: it says that no single
// signer, and no single country, may account for more than so much of a day, and
// leaves governance to decide what actually happened. What it buys is that the
// damage between the compromise and the revocation is finite and known, instead
// of being whatever the attacker managed before a human looked at a dashboard.
//
// It fails soft on purpose. Over the cap is a deferral — retry tomorrow — not a
// ban, because the alternative is that one stolen key locks every genuine holder
// of a country's passports out of the chain until a governance vote completes.

// dayOf is the unix day a block time falls in, the bucket every counter uses.
func dayOf(blockTime int64) uint64 { return uint64(blockTime) / 86400 }

// counterFor reads a counter and rolls it to the given day if it is stale,
// returning the count that applies to that day.
//
// Rolling in memory rather than writing here keeps this usable from the
// read-only check as well as from the increment: a counter that is never
// incremented again never needs to be written again.
func counterFor(c types.RateCounter, day uint64) types.RateCounter {
	if c.Day == day {
		return c
	}
	// One day on, today's count becomes yesterday's. More than one day on, the
	// day in between genuinely had nothing, so yesterday's total is zero rather
	// than a stale figure from whenever the subject was last active.
	prev := uint64(0)
	if c.Day+1 == day {
		prev = c.Count
	}
	return types.RateCounter{Day: day, Count: 0, PreviousCount: prev}
}

func (k Keeper) getRateCounter(ctx context.Context, get func() (types.RateCounter, error), day uint64) (types.RateCounter, error) {
	c, err := get()
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return types.RateCounter{}, err
		}
		c = types.RateCounter{}
	}
	return counterFor(c, day), nil
}

// networkPreviousDay returns how many registrations the whole network recorded
// on the last completed day, which is what the caps scale against.
func (k Keeper) networkPreviousDay(ctx context.Context, day uint64) (uint64, error) {
	c, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.NetworkRate.Get(ctx) }, day)
	if err != nil {
		return 0, err
	}
	return c.PreviousCount, nil
}

// checkRegistrationRate rejects a registration that would take its Document
// Signer, or its issuing country, past the day's allowance.
//
// Read-only: nothing is counted here. The count moves in recordRegistrationRate
// after the proof has verified and been bound to the certificate, because until
// then the signer named by the submission is only claimed. Counting first would
// let anyone burn a legitimate signer's daily allowance by submitting junk that
// names it — turning the defence into the denial-of-service it was meant to
// prevent.
func (k Keeper) checkRegistrationRate(ctx context.Context, dscKey []byte, country string) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	day := dayOf(sdk.UnwrapSDKContext(ctx).BlockTime().Unix())
	prev, err := k.networkPreviousDay(ctx, day)
	if err != nil {
		return err
	}

	if len(dscKey) > 0 {
		c, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.DscRate.Get(ctx, dscKey) }, day)
		if err != nil {
			return err
		}
		if cap := params.DscDailyCap(prev); c.Count >= cap {
			return types.ErrRegistrationRateLimited.Wrapf(
				"document signer has reached its %d registrations for today; retry tomorrow", cap)
		}
	}
	if country != "" {
		c, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.CountryRate.Get(ctx, country) }, day)
		if err != nil {
			return err
		}
		if cap := params.CountryDailyCap(prev); c.Count >= cap {
			return types.ErrRegistrationRateLimited.Wrapf(
				"country %s has reached its %d registrations for today; retry tomorrow", country, cap)
		}
	}
	return nil
}

// recordRegistrationRate counts a verified registration against its signer, its
// country and the network, and emits a warning as a subject approaches its cap.
//
// The warning is the point of the whole mechanism as much as the cap is. A cap
// that is silently hit tells nobody anything; the event is what turns "a signer
// is behaving strangely" into something an operator sees before governance has
// to decide whether to revoke.
func (k Keeper) recordRegistrationRate(ctx context.Context, dscKey []byte, country string) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	day := dayOf(sdkCtx.BlockTime().Unix())

	network, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.NetworkRate.Get(ctx) }, day)
	if err != nil {
		return err
	}
	prev := network.PreviousCount
	network.Count++
	if err := k.NetworkRate.Set(ctx, network); err != nil {
		return err
	}

	if len(dscKey) > 0 {
		c, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.DscRate.Get(ctx, dscKey) }, day)
		if err != nil {
			return err
		}
		c.Count++
		if err := k.DscRate.Set(ctx, dscKey, c); err != nil {
			return err
		}
		emitRateWarning(sdkCtx, "dsc", hexOf(dscKey), c.Count, params.DscDailyCap(prev))
	}
	if country != "" {
		c, err := k.getRateCounter(ctx, func() (types.RateCounter, error) { return k.CountryRate.Get(ctx, country) }, day)
		if err != nil {
			return err
		}
		c.Count++
		if err := k.CountryRate.Set(ctx, country, c); err != nil {
			return err
		}
		emitRateWarning(sdkCtx, "country", country, c.Count, params.CountryDailyCap(prev))
	}
	return nil
}

// rateWarningBps is how far into a subject's daily allowance it has to get
// before the chain says so: 80%. Early enough that an operator can look into it
// while registrations are still being accepted, late enough that ordinary busy
// days stay quiet.
const rateWarningBps = 8_000

func emitRateWarning(ctx sdk.Context, subjectKind, subject string, count, cap uint64) {
	if cap == 0 || count*types.BpsDenominator < cap*rateWarningBps {
		return
	}
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"registration_rate_warning",
			sdk.NewAttribute("subject_kind", subjectKind),
			sdk.NewAttribute("subject", subject),
			sdk.NewAttribute("count_today", strconv.FormatUint(count, 10)),
			sdk.NewAttribute("daily_cap", strconv.FormatUint(cap, 10)),
		),
	)
}
