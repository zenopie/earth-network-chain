package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/deflation/types"
)

// indexPrecision scales the allocation reward index to preserve fractional
// per-weight rewards (mirrors the reference contract's INDEX_PRECISION).
var indexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

// --- item getters with zero defaults ---

func (k Keeper) getRewardIndex(ctx context.Context) (math.Int, error) {
	v, err := k.RewardIndex.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getTotalWeight(ctx context.Context) (math.Int, error) {
	v, err := k.TotalWeight.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getLastAllocUpkeep(ctx context.Context) (int64, error) {
	v, err := k.LastAllocUpkeep.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// advanceAllocationIndex advances the global reward index by the allocation
// emission (1 ERTH/sec) accrued since the last upkeep, split across the total
// voting weight. Port of the reference `update_reward_index`.
func (k Keeper) advanceAllocationIndex(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	last, err := k.getLastAllocUpkeep(ctx)
	if err != nil {
		return err
	}
	if last != 0 && now > last {
		total, err := k.getTotalWeight(ctx)
		if err != nil {
			return err
		}
		if total.IsPositive() {
			reward := math.NewInt(types.EmissionPerSecond).MulRaw(now - last).QuoRaw(int64(time.Second))
			if reward.IsPositive() {
				idx, err := k.getRewardIndex(ctx)
				if err != nil {
					return err
				}
				idx = idx.Add(reward.Mul(indexPrecision).Quo(total))
				if err := k.RewardIndex.Set(ctx, idx); err != nil {
					return err
				}
			}
		}
	}
	return k.LastAllocUpkeep.Set(ctx, now)
}

// appendOption stores a new allocation option, assigning it the next id and
// initializing its accounting fields to the current reward index.
func (k Keeper) appendOption(ctx context.Context, opt types.AllocationOption) (uint64, error) {
	id, err := k.AllocationSeq.Next(ctx)
	if err != nil {
		return 0, err
	}
	rewardIndex, err := k.getRewardIndex(ctx)
	if err != nil {
		return 0, err
	}
	opt.Id = id
	opt.AmountAllocated = math.ZeroInt()
	opt.Accumulated = math.ZeroInt()
	opt.LastRewardIndex = rewardIndex
	if err := k.AllocationOptions.Set(ctx, id, opt); err != nil {
		return 0, err
	}
	if opt.Kind == types.ALLOCATION_KIND_INTEGRATED {
		if err := k.IntegratedOptions.Set(ctx, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// settleOption accrues an option's pending ERTH up to rewardIndex. Port of the
// reference `settle_allocation`.
func settleOption(opt *types.AllocationOption, rewardIndex math.Int) {
	if opt.AmountAllocated.IsPositive() {
		delta := rewardIndex.Sub(opt.LastRewardIndex)
		opt.Accumulated = opt.Accumulated.Add(opt.AmountAllocated.Mul(delta).Quo(indexPrecision))
	}
	opt.LastRewardIndex = rewardIndex
}

// resyncVoter re-applies a voter's allocation split at a (possibly new) stake
// weight: it removes the voter's previous contribution from each option and
// total, then adds the new contribution. The reward index must already be
// current (call advanceAllocationIndex first). Ports `subtract_old_allocations`
// + `add_new_allocations`.
func (k Keeper) resyncVoter(ctx context.Context, addrBz []byte, percentages []types.AllocationWeight, weight math.Int) error {
	// Callers hand in live bonded stake; the stream stores normalized units. Doing
	// the conversion here rather than at each call site is what keeps a voter who
	// touches their delegation on the same footing as one who has not voted since
	// before the stake compounded.
	weight, err := k.earthKeeper.NormalizeStakeWeight(ctx, weight)
	if err != nil {
		return err
	}

	rewardIndex, err := k.getRewardIndex(ctx)
	if err != nil {
		return err
	}
	total, err := k.getTotalWeight(ctx)
	if err != nil {
		return err
	}

	epoch, err := k.allocationEpoch(ctx)
	if err != nil {
		return err
	}

	// Remove the voter's previous contribution, but only if it belongs to the
	// current epoch. A reset already zeroed the aggregates wholesale, so
	// subtracting a stale record's weight again would drive options negative.
	if old, err := k.Voters.Get(ctx, addrBz); err == nil && old.Epoch == epoch {
		for _, w := range old.Percentages {
			opt, err := k.AllocationOptions.Get(ctx, w.OptionId)
			if err != nil {
				continue
			}
			settleOption(&opt, rewardIndex)
			amt := old.Weight.MulRaw(int64(w.Percent)).QuoRaw(100)
			opt.AmountAllocated = opt.AmountAllocated.Sub(amt)
			total = total.Sub(amt)
			if err := k.AllocationOptions.Set(ctx, w.OptionId, opt); err != nil {
				return err
			}
		}
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	// Add the new contribution.
	for _, w := range percentages {
		opt, err := k.AllocationOptions.Get(ctx, w.OptionId)
		if err != nil {
			return types.ErrOptionNotFound.Wrapf("option %d", w.OptionId)
		}
		settleOption(&opt, rewardIndex)
		amt := weight.MulRaw(int64(w.Percent)).QuoRaw(100)
		opt.AmountAllocated = opt.AmountAllocated.Add(amt)
		total = total.Add(amt)
		if err := k.AllocationOptions.Set(ctx, w.OptionId, opt); err != nil {
			return err
		}
	}

	if total.IsNegative() {
		total = math.ZeroInt()
	}
	if err := k.TotalWeight.Set(ctx, total); err != nil {
		return err
	}

	if len(percentages) == 0 || !weight.IsPositive() {
		return k.Voters.Remove(ctx, addrBz)
	}
	return k.Voters.Set(ctx, addrBz, types.Voter{Percentages: percentages, Weight: weight, Epoch: epoch})
}

// allocationEpoch returns the current allocation epoch (0 before any reset).
func (k Keeper) allocationEpoch(ctx context.Context) (uint64, error) {
	v, err := k.AllocationEpoch.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// resetAllocations retires every allocation vote at once: it settles each option
// up to now, zeroes the weights, and bumps the epoch so existing voter records
// stop counting.
//
// This is O(options), not O(voters) — the voter rows are left where they are and
// simply age out of relevance. Accrued balances are deliberately preserved: a
// reset redirects the future stream, it does not confiscate ERTH an option has
// already earned.
func (k Keeper) resetAllocations(ctx context.Context) (uint64, error) {
	if err := k.advanceAllocationIndex(ctx); err != nil {
		return 0, err
	}
	rewardIndex, err := k.getRewardIndex(ctx)
	if err != nil {
		return 0, err
	}

	var ids []uint64
	if err := k.AllocationOptions.Walk(ctx, nil, func(id uint64, _ types.AllocationOption) (bool, error) {
		ids = append(ids, id)
		return false, nil
	}); err != nil {
		return 0, err
	}
	for _, id := range ids {
		opt, err := k.AllocationOptions.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		// Settle before zeroing so everything earned up to this block is kept.
		settleOption(&opt, rewardIndex)
		opt.AmountAllocated = math.ZeroInt()
		if err := k.AllocationOptions.Set(ctx, id, opt); err != nil {
			return 0, err
		}
	}
	if err := k.TotalWeight.Set(ctx, math.ZeroInt()); err != nil {
		return 0, err
	}

	epoch, err := k.allocationEpoch(ctx)
	if err != nil {
		return 0, err
	}
	epoch++
	return epoch, k.AllocationEpoch.Set(ctx, epoch)
}
