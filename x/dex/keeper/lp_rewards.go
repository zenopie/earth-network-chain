package keeper

import (
	"context"
	"errors"
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
// The denominator is the sum of every pool's *stored* volume. A pool that goes
// quiet keeps its last recorded volume in that sum until something touches it
// and the daily decay is applied, so dormant pools pad the denominator slightly
// and under-pay active ones by a hair. That is the price of not walking the set;
// a pool dormant past the window decays to zero weight the moment it is touched.
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

// decayedVolume returns a pool's volume decayed to the given day. Each elapsed
// day multiplies the stored volume by (VolumeWindowDays-1)/VolumeWindowDays,
// giving a soft rolling window; after a full window of inactivity it is zero.
func decayedVolume(pool types.Pool, day uint64) math.Int {
	v := pool.Volume
	if v.IsNil() {
		return math.ZeroInt()
	}
	if pool.LastVolumeDay == 0 || day <= pool.LastVolumeDay {
		return v
	}
	daysPassed := day - pool.LastVolumeDay
	if daysPassed >= uint64(types.VolumeWindowDays) {
		return math.ZeroInt()
	}
	for d := uint64(0); d < daysPassed; d++ {
		v = v.MulRaw(types.VolumeWindowDays - 1).QuoRaw(types.VolumeWindowDays)
	}
	return v
}

func dayOf(blockTime time.Time) uint64 { return uint64(blockTime.Unix()) / 86400 }

// addPoolVolume decays a pool's volume to the current day, then adds new
// ERTH-denominated swap volume.
func addPoolVolume(pool *types.Pool, erthAmount math.Int, blockTime time.Time) {
	day := dayOf(blockTime)
	pool.Volume = decayedVolume(*pool, day).Add(erthAmount)
	pool.LastVolumeDay = day
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
	return delta.Mul(total).Quo(lpIndexPrecision), nil
}

// decayPoolVolume ages a pool's stored volume to blockTime's day and moves the
// global denominator by the same amount, keeping the two in step.
func (k Keeper) decayPoolVolume(ctx context.Context, pool *types.Pool, blockTime time.Time) error {
	if pool.Volume.IsNil() || !pool.Volume.IsPositive() {
		return nil
	}
	day := dayOf(blockTime)
	decayed := decayedVolume(*pool, day)
	if decayed.Equal(pool.Volume) {
		return nil
	}

	shrink := pool.Volume.Sub(decayed)
	pool.Volume = decayed
	pool.LastVolumeDay = day

	total, err := k.getLpTotalVolume(ctx)
	if err != nil {
		return err
	}
	total = total.Sub(shrink)
	if total.IsNegative() {
		total = math.ZeroInt()
	}
	return k.LpTotalVolume.Set(ctx, total)
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
	// Age the volume before anything is credited against it. A pool that has been
	// quiet must not collect the whole dormant stretch at the volume it last
	// recorded: that over-pays it, and it leaves a wash-trade opening where a
	// burst of self-dealing volume keeps earning at its peak rate for as long as
	// nobody touches the pool.
	if err := k.decayPoolVolume(ctx, pool, sdk.UnwrapSDKContext(ctx).BlockTime()); err != nil {
		return err
	}

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

	if vol := pool.Volume; !vol.IsNil() && vol.IsPositive() {
		owed := vol.Mul(idx.Sub(last)).Quo(lpIndexPrecision)
		if owed.IsPositive() {
			hub, err := k.HubDenom(ctx)
			if err != nil {
				return err
			}
			coin := sdk.NewCoin(hub, owed)
			if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, sdk.NewCoins(coin)); err != nil {
				return err
			}
			// The reward lands in the reserve, so every outstanding share earns a
			// slice of it simply by existing — including shares escrowed against an
			// in-flight unbonding. That is deliberate: the liquidity behind those
			// shares is still in the pool doing the work.
			pool.ReserveErth = pool.ReserveErth.Add(coin)
		}
	}
	return k.PoolLpIndex.Set(ctx, poolID, idx)
}

// applyVolume decays a pool's volume to today and adds new ERTH swap volume,
// keeping the global denominator in step with the change.
func (k Keeper) applyVolume(ctx context.Context, pool *types.Pool, erthAmount math.Int, blockTime time.Time) error {
	old := pool.Volume
	if old.IsNil() {
		old = math.ZeroInt()
	}
	addPoolVolume(pool, erthAmount, blockTime)

	delta := pool.Volume.Sub(old)
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

// initPoolLpIndex starts a new pool at the current index so it cannot collect
// rewards that accrued before it existed.
func (k Keeper) initPoolLpIndex(ctx context.Context, poolID uint64) error {
	idx, err := k.getLpRewardIndex(ctx)
	if err != nil {
		return err
	}
	return k.PoolLpIndex.Set(ctx, poolID, idx)
}
