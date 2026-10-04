package keeper

import (
	"context"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	earthtypes "github.com/earth-network/earth/x/earth/types"
	"github.com/earth-network/earth/x/personhood/types"

	"github.com/earth-network/earth/internal/safeexec"
)

// BeginBlocker retires lapsed and revoked registrations (zeroing their
// leaves), clears lapsed caretaker splits and released handles, prunes stale claim nullifiers and
// runs the ANML buyback-and-burn (1 ERTH/sec).
//
// It must run before x/allocation's BeginBlocker: clearing a lapsed split
// returns its weight to the caretaker stream, and doing that first is what
// makes this block's emission split across live splits only.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	// One budget for the whole block, shared by every kind of retirement.
	// BeginBlock runs on an infinite gas meter and consumes no block gas, so
	// this is the only ceiling on its work.
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	// Lapsed caretaker leases first, on a budget of their own: their weight
	// must be gone before x/allocation (next in BeginBlock) settles the
	// stream, and a backlog in the shared budget (a revoked signer's purge)
	// must not keep paying lapsed splits. Leases are cast at most one per
	// private action, so CaretakerSweepLimit per block outpaces any rate at
	// which they can lapse.
	k.runSweep(ctx, k.sweepCaretakerVotes, types.CaretakerSweepLimit)
	if err := k.runSweeps(ctx, params.RegistrationSweepLimitOrDefault()); err != nil {
		return err
	}
	if err := k.pruneClaimNullifiers(ctx, types.ClaimNullifierPruneLimit); err != nil {
		return err
	}

	// The buyback on its own branch, recovering panics (TWAP and quote maths
	// on dex state): a buyback that cannot run is skipped this block, its
	// emission still accrued, instead of halting the chain from BeginBlock.
	safeexec.Item(sdk.UnwrapSDKContext(ctx), types.ModuleName, "buyback", func(c sdk.Context) error {
		return k.buybackAndBurn(c)
	})
	return nil
}

// runSweep runs one sweep on its own branch, recovering panics: a sweep that
// fails retires nothing this block (used 0) and the others still run.
func (k Keeper) runSweep(ctx context.Context, sweep func(context.Context, int) (int, error), allowance int) int {
	used := 0
	if !safeexec.Item(sdk.UnwrapSDKContext(ctx), types.ModuleName, "sweep", func(c sdk.Context) error {
		u, err := sweep(c, allowance)
		used = u
		return err
	}) {
		return 0
	}
	return used
}

// sweepReserveDivisor sets each later sweep's guaranteed share of the block's
// retirement budget: budget/sweepReserveDivisor (at least 1) apiece for the
// expiry, caretaker, used-binding and handle sweeps.
const sweepReserveDivisor = 8

// runSweeps shares one block's retirement budget among the five sweeps.
//
// The revoked-signer purge comes first and gets the largest share (see
// purgeRevokedDscs for why it outranks expiry), but not all of it: the expiry,
// caretaker, used-binding and handle sweeps each have a reserved share, so a revoked
// signer with many registrations (a purge lasting many blocks) cannot starve
// them. A lapsed registration that keeps its leaf, a lapsed caretaker split
// that keeps its weight, or a released handle still reserved is
// each a wrong of its own, and none of them should wait on another's backlog.
//
// Round one runs each sweep in priority order with its share plus whatever the
// sweeps before it left unused. Round two hands what is still left, in the
// same order, to the sweeps that used their whole allowance (they may have
// more). The total never exceeds budget.
func (k Keeper) runSweeps(ctx context.Context, budget int) error {
	if budget <= 0 {
		return nil
	}
	sweeps := []func(context.Context, int) (int, error){
		k.purgeRevokedDscs,
		k.sweepExpiredRegistrations,
		k.sweepCaretakerVotes,
		k.sweepUsedBindings,
		k.sweepHandles,
	}
	reserve := budget / sweepReserveDivisor
	if reserve == 0 && budget >= len(sweeps) {
		reserve = 1
	}
	shares := make([]int, len(sweeps))
	shares[0] = budget - reserve*(len(sweeps)-1)
	for i := 1; i < len(shares); i++ {
		shares[i] = reserve
	}

	remaining := budget
	carry := 0
	saturated := make([]bool, len(sweeps))
	for i, sweep := range sweeps {
		allowance := shares[i] + carry
		if allowance > remaining {
			allowance = remaining
		}
		used := k.runSweep(ctx, sweep, allowance)
		remaining -= used
		carry = allowance - used
		saturated[i] = allowance > 0 && used >= allowance
	}
	for i, sweep := range sweeps {
		if remaining <= 0 {
			break
		}
		if !saturated[i] {
			continue
		}
		remaining -= k.runSweep(ctx, sweep, remaining)
	}
	return nil
}

// EndBlocker records the block's identity root as an anchor, if the tree
// moved, and prunes anchors past the window.
func (k Keeper) EndBlocker(ctx context.Context) error {
	if err := k.recordIdentityRoot(ctx); err != nil {
		return err
	}
	window, err := k.IdentityRootWindow(ctx)
	if err != nil {
		return err
	}
	return k.pruneIdentityRoots(ctx, window, types.IdentityRootPruneLimit)
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
	// Cap the trade itself. Whatever of the (capped) backlog this trade does
	// not spend stays accrued: the clock below advances only by what was
	// bought, so the rest is bought over the following windows, each at most
	// buyback_max_trade_seconds of emission.
	carried := int64(0)
	if maxTrade := params.BuybackMaxTradeSecondsOrDefault() * int64(time.Second); elapsed > maxTrade {
		carried = elapsed - maxTrade
		elapsed = maxTrade
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
	out, err := k.dexKeeper.SwapExactInForModule(cacheCtx, types.ModuleName, erthIn, types.AnmlDenom, minOut)
	if err != nil {
		return nil // discard: no write, emission stays accrued for the next window
	}
	if out.Amount.IsPositive() {
		if err := k.bankKeeper.BurnCoins(cacheCtx, types.ModuleName, sdk.NewCoins(out)); err != nil {
			return nil // discard
		}
		// On cacheCtx, not ctx: every path above discards the whole window on
		// failure, and a counter written outside the cache would survive that
		// and claim a buyback that never settled.
		if err := k.burnRecorder.RecordBurn(cacheCtx, earthtypes.SourceAnmlBuyback, sdk.NewCoins(out)); err != nil {
			return nil // discard
		}
	}
	write()

	// Only now that the trade has committed do the clock and the observation
	// move. Both are set on the success path alone: every early return above
	// leaves the accrual intact so a refused window is deferred, never dropped.
	if err := k.LastBuyback.Set(ctx, now-carried); err != nil {
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
