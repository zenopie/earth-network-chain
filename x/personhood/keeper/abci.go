package keeper

import (
	"context"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// BeginBlocker retires lapsed registrations and runs the ANML
// buyback-and-burn (1 ERTH/sec).
//
// It must run before x/allocation's BeginBlocker: the sweep returns a lapsed
// human's vote weight to the human stream, and doing that first is what makes
// this block's emission split across live humans only.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	// One retirement budget for the whole block, shared by both reasons a
	// registration gets retired.
	//
	// Nothing meters this. Block gas bounds transactions; BeginBlock runs on an
	// infinite gas meter and consumes no block gas, so a sweep that ran long
	// would not fail with an error — it would simply make the block take longer,
	// and enough of that is a liveness problem. Giving each sweep its own cap
	// would leave the number that decides how long a block takes, their sum,
	// chosen by nobody.
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	budget := params.RegistrationSweepLimitOrDefault()

	// Revoked signers first: see purgeRevokedDscs for why it outranks expiry.
	used, err := k.purgeRevokedDscs(ctx, budget)
	if err != nil {
		return err
	}
	if _, err := k.sweepExpiredRegistrations(ctx, budget-used); err != nil {
		return err
	}

	return k.buybackAndBurn(ctx)
}

func (k Keeper) getLastBuyback(ctx context.Context) (int64, error) {
	v, err := k.LastBuyback.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

func (k Keeper) moduleAddress() sdk.AccAddress {
	return authtypes.NewModuleAddress(types.ModuleName)
}

// buybackAndBurn mints the ERTH this pillar has emitted since the last buyback
// (1 ERTH/sec), swaps it for ANML on the dex, and burns the ANML. The
// mint->swap->burn is done atomically in a cache context so a missing pool or a
// rounding failure can never halt the block or leak funds.
//
// It does not run every block. Emission accrues until the price observation it
// prices against is at least a full window old, and only then does it trade, for
// the whole accrued amount at once. Two reasons, and they are the same reason:
//
//   - A buyback cannot be protected by an average it does not have. Averaging
//     over one block is averaging over the spot price, which is precisely the
//     number an attacker controls. The window has to be long enough that holding
//     the price away from its average for the whole of it costs more than the
//     trade being diverted is worth.
//
//   - A fixed-size market buy from a known address at a known moment is the
//     easiest order on the chain to trade against. Waiting does not make the
//     timing secret, but it does mean the price has to be held, not merely
//     nudged in one block and released in the next.
//
// Nothing is given up by waiting: the accrued amount is minted in full when the
// trade fires, so the pillar emits exactly what it would have emitted per block.
func (k Keeper) buybackAndBurn(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime().UnixNano()

	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	last, err := k.getLastBuyback(ctx)
	if err != nil {
		return err
	}
	if last == 0 {
		// First block: start the clock. Nothing accrued before the chain ran.
		return k.LastBuyback.Set(ctx, now)
	}
	if now <= last {
		return nil
	}

	// No pool means no price and nothing to buy with. Advance the clock rather
	// than accruing: emission that had nowhere to go was not earned, and this is
	// the state the chain sits in between genesis and the auction settling, which
	// must not build up a purchase to make the moment a pool appears.
	has, err := k.dexKeeper.HasPoolForToken(ctx, types.AnmlDenom)
	if err != nil {
		return err
	}
	erth, err := k.erthDenom(ctx)
	if err != nil {
		return err
	}
	if !has || erth == types.AnmlDenom {
		return k.LastBuyback.Set(ctx, now)
	}

	cum, spot, observedAt, err := k.dexKeeper.TwapObservation(ctx, types.AnmlDenom)
	if err != nil {
		// An unpriceable pool is the no-pool case: nothing to buy into.
		return k.LastBuyback.Set(ctx, now)
	}

	prevCum, prevAt, ok, err := k.getTwapObservation(ctx)
	if err != nil {
		return err
	}
	if !ok || observedAt <= prevAt {
		// Nothing to average against yet. Record the near end of the window and
		// accrue — deliberately without advancing the buyback clock, so the
		// emission from here to the first trade is kept rather than dropped.
		return k.setTwapObservation(ctx, cum, observedAt)
	}

	window := observedAt - prevAt
	if window < params.BuybackTwapWindowSecondsOrDefault() {
		return nil // still filling the window; keep accruing
	}

	// The average price over the window, in ERTH per ANML.
	twap := cum.Sub(prevCum).QuoInt64(window)
	if !twap.IsPositive() {
		return k.setTwapObservation(ctx, cum, observedAt)
	}

	// The gate. Refuse to buy into a pool whose spot price has been pushed above
	// its own average by more than the tolerance.
	//
	// This is what makes the buyback unprofitable to sandwich. An attacker who
	// runs the price up ahead of it does not get a protocol buying high — the
	// trade simply does not happen, the emission stays accrued, and they are left
	// holding ANML they bought above its average price. The next window prices
	// against the average that their own manipulation has since decayed out of.
	//
	// One-sided on purpose: a spot price BELOW the average means the buyback
	// gets more ANML per ERTH, which is the outcome this mechanism exists to
	// produce. Gating on it would stall emission to prevent a good trade, and an
	// attacker pushing the price down is selling ANML cheaply to the buyer.
	maxDev := params.BuybackMaxDeviationBpsOrDefault()
	ceiling := twap.Mul(math.LegacyNewDec(types.BpsDenominator + maxDev)).QuoInt64(types.BpsDenominator)
	if spot.GT(ceiling) {
		// Roll the near end of the window forward but keep accruing. Rolling
		// matters: the observation must keep advancing or the window grows
		// without bound and the average eventually covers a stretch too long to
		// describe the current price at all.
		if err := k.setTwapObservation(ctx, cum, observedAt); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(
			sdk.NewEvent(
				"anml_buyback_skipped",
				sdk.NewAttribute("reason", "spot above twap"),
				sdk.NewAttribute("spot", spot.String()),
				sdk.NewAttribute("twap", twap.String()),
				sdk.NewAttribute("max_deviation_bps", strconv.FormatInt(maxDev, 10)),
			),
		)
		return nil
	}

	// Cap the catch-up. Accruing is what makes a skipped window harmless, but it
	// also means a halt, or a long run of windows the gate refused, would
	// otherwise arrive as one unbounded market order. Beyond the cap the emission
	// is simply not minted — the same answer genesis gives for a chain that was
	// not running: no time was served, so none is owed.
	elapsed := now - last
	if maxAccrual := params.BuybackMaxAccrualSecondsOrDefault() * int64(time.Second); elapsed > maxAccrual {
		elapsed = maxAccrual
	}
	amount := math.NewInt(types.EmissionPerSecond).MulRaw(elapsed).QuoRaw(int64(time.Second))
	if !amount.IsPositive() {
		return nil
	}

	// min_out from a quote of this exact trade against the current depth, less a
	// small tolerance. Taking it from the quote rather than from the average is
	// what keeps a thin pool working: a buy large relative to the reserves moves
	// the price along the curve by design, and a min_out derived from the average
	// alone would read that honest impact as failure and never fill. The gate
	// above has already established that the depth being quoted against is not a
	// manipulated one.
	quoted, err := k.dexKeeper.QuoteHubToToken(ctx, types.AnmlDenom, amount)
	if err != nil {
		return nil // unquotable: leave the clock alone and retry next block
	}
	minOut := quoted.MulRaw(types.BpsDenominator - types.BuybackQuoteToleranceBps).QuoRaw(types.BpsDenominator)
	if !minOut.IsPositive() {
		return nil
	}

	cacheCtx, write := sdkCtx.CacheContext()
	erthIn := sdk.NewCoin(erth, amount)
	if err := k.bankKeeper.MintCoins(cacheCtx, types.ModuleName, sdk.NewCoins(erthIn)); err != nil {
		return nil // discard
	}
	out, err := k.dexKeeper.SwapExactIn(cacheCtx, k.moduleAddress(), erthIn, types.AnmlDenom, minOut)
	if err != nil {
		return nil // discard: no write, emission stays accrued for the next window
	}
	if out.Amount.IsPositive() {
		if err := k.bankKeeper.BurnCoins(cacheCtx, types.ModuleName, sdk.NewCoins(out)); err != nil {
			return nil // discard
		}
	}
	write()

	// Only now that the trade has committed do the clock and the observation
	// move. Both are set on the success path alone: every early return above
	// leaves the accrual intact so a refused window is deferred, never dropped.
	if err := k.LastBuyback.Set(ctx, now); err != nil {
		return err
	}
	if err := k.setTwapObservation(ctx, cum, observedAt); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			"anml_buyback_burn",
			sdk.NewAttribute("erth_spent", erthIn.String()),
			sdk.NewAttribute("anml_burned", out.String()),
			sdk.NewAttribute("twap", twap.String()),
			sdk.NewAttribute("min_out", minOut.String()),
			sdk.NewAttribute("window_seconds", strconv.FormatInt(window, 10)),
		),
	)
	return nil
}

// getTwapObservation returns the stored price observation. ok is false before
// the first one is recorded, which is the state a fresh chain and a chain
// restarted from an export both start in.
func (k Keeper) getTwapObservation(ctx context.Context) (cum math.LegacyDec, at int64, ok bool, err error) {
	at, err = k.TwapObservedAt.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.LegacyDec{}, 0, false, nil
		}
		return math.LegacyDec{}, 0, false, err
	}
	cum, err = k.TwapObservation.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.LegacyDec{}, 0, false, nil
		}
		return math.LegacyDec{}, 0, false, err
	}
	return cum, at, true, nil
}

// setTwapObservation records the near end of the averaging window.
func (k Keeper) setTwapObservation(ctx context.Context, cum math.LegacyDec, at int64) error {
	if err := k.TwapObservation.Set(ctx, cum); err != nil {
		return err
	}
	return k.TwapObservedAt.Set(ctx, at)
}
