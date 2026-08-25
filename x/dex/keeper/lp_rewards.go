package keeper

import (
	"context"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// --- volume-weighted LP reward distribution ---
//
// The stake-weighted allocation stream lives in x/allocation; when its
// LP-rewards option accrues ERTH, x/allocation calls DistributeLPRewards below.
// That call is O(1): it only advances a global index. Pools collect what they
// are owed lazily, the next time a swap or liquidity change touches them, using
// the same index pattern as the allocation streams (see x/allocation/keeper/
// allocation.go). Nothing iterates the pool set, so creating pools is a cost
// borne only by whoever creates them.
//
// Volume is stored SCALED, not decayed. There is one global VolumeIndex that
// grows by VolumeDecayWindow/(VolumeDecayWindow-1) each day, and a pool records
// `traded * index` rather than `traded`. A reward share is then
// `pool.VolumeWeight / LpTotalVolume`, in which the index cancels — so the shares are
// exact at every instant and nothing has to be aged.
//
// That is what fixes the accounting, and it is worth being explicit about what
// was wrong. Volume used to decay per pool, applied only when something touched
// that pool, while LpTotalVolume kept the undecayed figure the whole time. The
// numerator shrank and the denominator did not, so a pool was credited against a
// total that no longer described it: measured over a year of mixed pool
// activity, 9-11% of the LP emission was released by the allocation stream and
// collected by nobody. Inflating new volume instead of shrinking old volume
// gives the identical ratio — a pool that traded a week ago is worth
// (13/14)^7 of one trading today, either way — while leaving every stored
// number untouched between trades.
//
// Nothing decays to zero on its own under that scheme, so a pool nobody trades
// would hold a dwindling share of the denominator forever. Trading therefore
// starts a timer: PoolStaleSeconds after its last recorded volume a pool is
// swept, its volume zeroed and its weight removed from the denominator. Trading
// again pushes the timer out. The queue is ordered by due time and the sweep is
// capped per block, so it costs nothing to have many pools and the set is never
// walked.
//
// LpTotalVolume must stay equal to the sum of every pool's stored Volume.
// DistributeLPRewards reports back delta*LpTotalVolume as the amount it handed
// out, and the pools between them claim delta*sum(Volume); if the two drift
// apart the module either mints more than the allocation released or strands
// part of it. Every write to a pool's Volume therefore moves LpTotalVolume by
// the same amount, which is why the decay lives on the keeper and not in the
// pure helper below.

// lpIndexPrecision scales the LP reward index to preserve fractional
// per-volume rewards (matches indexPrecision in the allocation streams).
var lpIndexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

// volumeIndexPrecision is the scale VolumeIndex is carried at. It starts here
// and only ever grows.
var volumeIndexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

// getVolumeIndex reads the scaling index, defaulting to 1.0 before the first
// advance rather than to zero — a zero index would make every trade weightless.
func (k Keeper) getVolumeIndex(ctx context.Context) (math.Int, error) {
	v, err := k.VolumeIndex.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return volumeIndexPrecision, nil
		}
		return math.Int{}, err
	}
	if v.IsNil() || !v.IsPositive() {
		return volumeIndexPrecision, nil
	}
	return v, nil
}

// The index only ever grows, and nothing here shrinks it back.
//
// That is deliberate rather than overlooked. At 14/13 a day it passes 2^128 in
// about 21 months and 2^256 in about five years, which costs a few extra machine
// words per multiply and a few dozen bytes per pool — real but not interesting,
// and math.Int has no ceiling, so nothing breaks if it is never dealt with.
//
// If it is ever worth tidying, it belongs in an upgrade handler and not in
// consensus: a migration runs once at a known height on every node and may walk
// every pool, which is the budget an in-block rebase does not have. The order
// matters:
//
//  1. settle every pool first. Owed rewards are Volume*(index-poolIndex); divide
//     Volume without settling and every unsettled reward is underpaid by exactly
//     the rebase factor.
//  2. divide VolumeIndex and every pool's Volume by the same factor.
//  3. recompute LpTotalVolume by summing the pools. Do NOT divide it on its own —
//     the sum of truncated divisions is not the truncated division of the sum,
//     and CheckVolumeAccounting compares the two every block.
//  4. drop pools whose volume truncated to zero from the stale queue.
//
// Shares are ratios, so a uniform division leaves every pool earning what it
// earned before.

// AdvanceVolumeIndex grows the scaling index by one step per whole day elapsed.
//
// One step is VolumeDecayWindowDays/(VolumeDecayWindowDays-1), which is the
// reciprocal of the decay it replaces: making today's volume worth 14/13 of
// yesterday's is the same statement as yesterday's being worth 13/14 of today's,
// and it costs one multiplication for the whole chain instead of one per pool.
//
// The catch-up loop is bounded by volumeIndexMaxCatchUpDays so that a chain
// resuming from a stale genesis_time cannot spend a block compounding thousands
// of steps. Past a couple of months of downtime every live pool has been swept
// as stale anyway, so the steps being skipped have nothing left to act on.
func (k Keeper) AdvanceVolumeIndex(ctx context.Context) error {
	day := dayOf(sdk.UnwrapSDKContext(ctx).BlockTime())

	last, err := k.VolumeIndexDay.Get(ctx)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		last = 0
	}
	if last == 0 {
		// First block this has seen: anchor the index without compounding from
		// the unix epoch.
		return k.VolumeIndexDay.Set(ctx, day)
	}
	if day <= last {
		return nil
	}

	steps := day - last
	if steps > types.VolumeIndexMaxCatchUpDays {
		steps = types.VolumeIndexMaxCatchUpDays
	}
	idx, err := k.getVolumeIndex(ctx)
	if err != nil {
		return err
	}
	for i := uint64(0); i < steps; i++ {
		idx = idx.MulRaw(types.VolumeDecayWindowDays).QuoRaw(types.VolumeDecayWindowDays - 1)
	}
	if err := k.VolumeIndex.Set(ctx, idx); err != nil {
		return err
	}
	return k.VolumeIndexDay.Set(ctx, day)
}

func dayOf(blockTime time.Time) uint64 { return uint64(blockTime.Unix()) / 86400 }

// volumeCap is the most volume a pool may count toward LP rewards: a multiple of
// its own ERTH reserve.
//
// The multiple is per day, but the value it bounds is the decaying accumulator,
// which carries about VolumeWindowDays of volume once it settles (each day keeps
// (n-1)/n of the total and adds a fresh day, so a steady d per day converges to
// n*d). Scaling by the window is what makes "2 per day" mean two per day rather
// than two per week.
//
// A pool with no ERTH reserve caps at zero: there is no depth to justify any
// weight, and a pool in that state cannot be traded against anyway.
// The window here is VolumeWindowDays, deliberately NOT the decay window. The
// two used to be one constant and they answer different questions: this one asks
// how much volume a given depth can justify, the decay asks how far back trading
// counts. Slowing the decay must not quietly double what a thin pool may claim.
func volumeCap(pool types.Pool, perDay uint64) math.Int {
	r := pool.ReserveErth.Amount
	if r.IsNil() || !r.IsPositive() {
		return math.ZeroInt()
	}
	return r.MulRaw(int64(perDay)).MulRaw(types.VolumeWindowDays)
}

// capVolume clamps a candidate SCALED volume to what the pool's depth supports.
//
// The cap is a statement about real volume, so it is scaled by the current index
// to be compared against a stored figure. Both sides move with the index and the
// ratio the cap expresses does not.
func (k Keeper) capVolume(ctx context.Context, pool types.Pool, scaled math.Int) (math.Int, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return math.Int{}, err
	}
	idx, err := k.getVolumeIndex(ctx)
	if err != nil {
		return math.Int{}, err
	}
	cap := volumeCap(pool, params.VolumeDepthCapPerDayOrDefault()).Mul(idx).Quo(volumeIndexPrecision)
	if scaled.GT(cap) {
		return cap, nil
	}
	return scaled, nil
}

// setPoolVolume writes a pool's counted volume and moves the global denominator
// by exactly the same delta.
//
// Every write to a pool's Volume goes through here. LpTotalVolume has to stay
// equal to the sum of every pool's stored Volume — DistributeLPRewards divides
// by it, so a drift either mints more than the allocation released or strands
// part of it — and that equality was previously re-established by hand at each
// write site. One funnel means a new write site cannot forget it.
func (k Keeper) setPoolVolume(ctx context.Context, pool *types.Pool, v math.Int, day uint64) error {
	old := pool.VolumeWeight
	if old.IsNil() {
		old = math.ZeroInt()
	}
	if v.IsNegative() {
		v = math.ZeroInt()
	}
	pool.VolumeWeight = v
	pool.LastTradedDay = day

	delta := v.Sub(old)
	if delta.IsZero() {
		return nil
	}
	total, err := k.getLpTotalVolume(ctx)
	if err != nil {
		return err
	}
	total = total.Add(delta)
	if total.IsNegative() {
		total = math.ZeroInt()
	}
	return k.LpTotalVolume.Set(ctx, total)
}

// --- index getters with zero defaults ---

func (k Keeper) getLpRewardIndex(ctx context.Context) (math.Int, error) {
	v, err := k.LpRewardIndex.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getLpTotalVolume(ctx context.Context) (math.Int, error) {
	v, err := k.LpTotalVolume.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getPoolLpIndex(ctx context.Context, poolID uint64) (math.Int, error) {
	v, err := k.PoolLpIndex.Get(ctx, poolID)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

// DistributeLPRewards is the exported entry point used by the x/allocation module
// to auto-compound the LP-rewards allocation option into the dex pools. It
// advances the global reward index by amount/totalVolume and returns how much of
// `amount` that actually represents; the remainder (all of it when no pool has
// volume yet) is left for the caller to carry forward.
func (k Keeper) DistributeLPRewards(ctx context.Context, amount math.Int) (math.Int, error) {
	if !amount.IsPositive() {
		return math.ZeroInt(), nil
	}
	total, err := k.getLpTotalVolume(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	if !total.IsPositive() {
		return math.ZeroInt(), nil // carry forward until volume exists
	}

	delta := amount.Mul(lpIndexPrecision).Quo(total)
	if !delta.IsPositive() {
		return math.ZeroInt(), nil // too small to move the index; carry forward
	}
	idx, err := k.getLpRewardIndex(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	if err := k.LpRewardIndex.Set(ctx, idx.Add(delta)); err != nil {
		return math.ZeroInt(), err
	}
	// Report back only what the (truncated) index bump really represents, so the
	// rounding dust stays in the option and is not silently written off.
	resolved := delta.Mul(total).Quo(lpIndexPrecision)

	// Book it as owed before the caller transfers it in. The index has moved, so
	// from this instant the pools are collectively owed `resolved` whether or not
	// any of them has settled — and the module is holding coins that belong to
	// them rather than to itself.
	pending, err := k.getPendingLpRewards(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	if err := k.PendingLpRewards.Set(ctx, pending.Add(resolved)); err != nil {
		return math.ZeroInt(), err
	}
	return resolved, nil
}

func (k Keeper) getPendingLpRewards(ctx context.Context) (math.Int, error) {
	v, err := k.PendingLpRewards.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	if v.IsNil() {
		return math.ZeroInt(), nil
	}
	return v, nil
}

// scheduleStale puts a pool on the staleness clock, or takes it off.
//
// A pool carrying volume is due to be swept PoolStaleSeconds after this call;
// recording volume again moves the due date out, so only a pool that stops
// trading altogether ever comes due. A pool with no volume left is taken off the
// queue entirely, because there is nothing for the sweep to remove.
//
// Unlike the option prune schedule this DOES push the due date out on every
// write, and for the opposite reason: an option is scheduled because it is dead
// and must not be able to postpone its own removal, whereas a pool is scheduled
// because it is alive and trading is exactly the evidence that should keep it.
func (k Keeper) scheduleStale(ctx context.Context, poolID uint64, hasVolume bool) error {
	due, err := k.PoolStaleDue.Get(ctx, poolID)
	scheduled := err == nil
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if scheduled {
		if err := k.PoolStaleQueue.Remove(ctx, collections.Join(due, poolID)); err != nil {
			return err
		}
		if err := k.PoolStaleDue.Remove(ctx, poolID); err != nil {
			return err
		}
	}
	if !hasVolume {
		return nil
	}
	at := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + types.PoolStaleSeconds
	if err := k.PoolStaleDue.Set(ctx, poolID, at); err != nil {
		return err
	}
	return k.PoolStaleQueue.Set(ctx, collections.Join(at, poolID))
}

// SweepStalePools drops the weight of pools that have stopped trading.
//
// Scaled volume never decays to nothing by itself — that is the point of the
// scheme — so without this a pool nobody has traded in years would keep a
// dwindling but non-zero slice of every LP reward, and its row would never go
// away. The timer is PoolStaleSeconds, long enough that a pool is already worth
// (13/14)^60 ≈ 1% of its weight by the time it is swept: the sweep is
// bookkeeping, not a cliff anyone earns their way off.
//
// Bounded the same way SweepPrunableOptions is: it stops at the first entry that
// is not due, because the queue is ordered by due time, and stops after
// PoolStaleSweepLimit removals whatever else is waiting.
func (k Keeper) SweepStalePools(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()

	due := make([]collections.Pair[int64, uint64], 0, types.PoolStaleSweepLimit)
	iter, err := k.PoolStaleQueue.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		entry, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		if entry.K1() > now {
			break // ordered by due time: nothing after this is due either
		}
		if len(due) == types.PoolStaleSweepLimit {
			break
		}
		due = append(due, entry)
	}
	iter.Close()

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, entry := range due {
		poolID := entry.K2()
		if err := k.PoolStaleQueue.Remove(ctx, entry); err != nil {
			return err
		}
		if err := k.PoolStaleDue.Remove(ctx, poolID); err != nil {
			return err
		}

		pool, err := k.Pool.Get(ctx, poolID)
		if errors.Is(err, collections.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if pool.VolumeWeight.IsNil() || !pool.VolumeWeight.IsPositive() {
			continue
		}
		// Settle first. The pool held this weight right up to now, and the
		// rewards it earned doing so are owed to its LPs — dropping the weight
		// without crediting them would strand exactly what this whole scheme
		// exists to stop stranding.
		if err := k.settlePoolRewards(ctx, poolID, &pool); err != nil {
			return err
		}
		if err := k.setPoolVolume(ctx, &pool, math.ZeroInt(), dayOf(sdkCtx.BlockTime())); err != nil {
			return err
		}
		if err := k.SetPool(ctx, poolID, pool); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"pool_volume_expired",
			sdk.NewAttribute("pool_id", strconv.FormatUint(poolID, 10)),
		))
	}
	return nil
}

// settlePoolRewards credits a pool the LP rewards accrued against its volume
// since it was last touched, minting the ERTH straight into its reserve so the
// rewards compound for existing LPs.
//
// Every entry point that prices against the reserves or issues/redeems shares
// must call this first. Otherwise pending rewards sit outside the reserve where
// a latecomer could add liquidity, wait for the next swap to settle them in, and
// withdraw a share of rewards earned before they arrived.
func (k Keeper) settlePoolRewards(ctx context.Context, poolID uint64, pool *types.Pool) error {
	// Book the price the pool has been holding before this call changes it.
	//
	// Compounding rewards adds ERTH to the reserve and nothing to the other
	// side, so it moves the price. The interval that just elapsed belongs to the
	// price the pool actually had over it, and crediting it afterwards would
	// attribute the new price to time that had already passed.
	//
	// It lives here rather than at the call sites because this is the thing that
	// moves the price. Guarding the callers instead left add-liquidity, the
	// unbonding payout and the POL burn unguarded -- price-neutral operations
	// themselves, but each one calls this, and this is not price-neutral. Every
	// entry point that touches a pool already has to call settle first, so
	// putting the advance inside it is what makes the guard impossible to omit.
	if err := k.advancePriceCumulative(ctx, poolID, *pool); err != nil {
		return err
	}

	// Nothing is aged here any more, and that is the fix rather than an omission.
	// Ageing on touch is what made the numerator smaller than the denominator the
	// index was built from; a scaled volume is already worth what it should be
	// worth, whenever it is read.

	idx, err := k.getLpRewardIndex(ctx)
	if err != nil {
		return err
	}
	last, err := k.getPoolLpIndex(ctx, poolID)
	if err != nil {
		return err
	}
	if idx.Equal(last) {
		return nil
	}

	if vol := pool.VolumeWeight; !vol.IsNil() && vol.IsPositive() {
		owed := vol.Mul(idx.Sub(last)).Quo(lpIndexPrecision)
		if owed.IsPositive() {
			hub, err := k.HubDenom(ctx)
			if err != nil {
				return err
			}
			// Nothing is minted here. x/allocation issued this ERTH when the
			// stream's index advanced and paid it into this module's account;
			// all that is left is to move it out of the pending pile and into
			// the reserve it was always for.
			pending, err := k.getPendingLpRewards(ctx)
			if err != nil {
				return err
			}
			if err := k.PendingLpRewards.Set(ctx, pending.Sub(owed)); err != nil {
				return err
			}
			// The reward lands in the reserve, so every outstanding share earns a
			// slice of it simply by existing — including shares escrowed against an
			// in-flight unbonding. That is deliberate: the liquidity behind those
			// shares is still in the pool doing the work.
			pool.ReserveErth = pool.ReserveErth.Add(sdk.NewCoin(hub, owed))
		}
	}
	return k.PoolLpIndex.Set(ctx, poolID, idx)
}

// applyVolume adds new ERTH swap volume to a pool, scaled by the current index,
// and clamps the result to what the pool's depth supports.
//
// Scaling on the way in is the whole mechanism: `erthAmount * index` is worth
// more the later it is recorded, which is what makes older volume count for
// less without anything having to go back and reduce it.
func (k Keeper) applyVolume(ctx context.Context, pool *types.Pool, erthAmount math.Int, blockTime time.Time) error {
	// Advanced here rather than from a BeginBlocker because this is the only
	// place the index is consumed. Its value is a function of the block's day,
	// not of who trades, so advancing on first use is deterministic — and a
	// chain with no trading pays nothing to keep a number nobody is reading.
	if err := k.AdvanceVolumeIndex(ctx); err != nil {
		return err
	}
	idx, err := k.getVolumeIndex(ctx)
	if err != nil {
		return err
	}
	old := pool.VolumeWeight
	if old.IsNil() {
		old = math.ZeroInt()
	}
	v, err := k.capVolume(ctx, *pool, old.Add(erthAmount.Mul(idx).Quo(volumeIndexPrecision)))
	if err != nil {
		return err
	}
	if err := k.setPoolVolume(ctx, pool, v, dayOf(blockTime)); err != nil {
		return err
	}
	// Trading is what starts the timer, so it is recorded here rather than in
	// setPoolVolume: the sweep itself writes a volume of zero through that
	// funnel, and must not thereby reschedule the pool it is retiring.
	return k.scheduleStale(ctx, pool.PoolId, v.IsPositive())
}

// initPoolLpIndex starts a new pool at the current index so it cannot collect
// rewards that accrued before it existed.
func (k Keeper) initPoolLpIndex(ctx context.Context, poolID uint64) error {
	idx, err := k.getLpRewardIndex(ctx)
	if err != nil {
		return err
	}
	return k.PoolLpIndex.Set(ctx, poolID, idx)
}
