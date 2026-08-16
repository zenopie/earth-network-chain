package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// indexPrecision scales the democratic reward index to preserve fractional rewards.
var indexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

func (k Keeper) erthDenom(ctx context.Context) (string, error) {
	return k.dexKeeper.HubDenom(ctx)
}

// --- item getters with zero defaults ---

func (k Keeper) demRewardIndex(ctx context.Context) (math.Int, error) {
	v, err := k.DemRewardIndex.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) demTotalWeight(ctx context.Context) (math.Int, error) {
	v, err := k.DemTotalWeight.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) demLastUpkeep(ctx context.Context) (int64, error) {
	v, err := k.DemLastUpkeep.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// advanceDemIndex advances the democratic reward index by the emission
// (1 ERTH/sec) accrued since the last upkeep, split across the total voting
// weight (one-human-one-vote).
func (k Keeper) advanceDemIndex(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	last, err := k.demLastUpkeep(ctx)
	if err != nil {
		return err
	}
	if last != 0 && now > last {
		total, err := k.demTotalWeight(ctx)
		if err != nil {
			return err
		}
		if total.IsPositive() {
			reward := math.NewInt(types.EmissionPerSecond).MulRaw(now - last).QuoRaw(int64(time.Second))
			if reward.IsPositive() {
				idx, err := k.demRewardIndex(ctx)
				if err != nil {
					return err
				}
				idx = idx.Add(reward.Mul(indexPrecision).Quo(total))
				if err := k.DemRewardIndex.Set(ctx, idx); err != nil {
					return err
				}
			}
		}
	}
	return k.DemLastUpkeep.Set(ctx, now)
}

// settleOption accrues an option's pending ERTH up to rewardIndex.
func settleOption(opt *types.DemocraticOption, rewardIndex math.Int) {
	if opt.AmountAllocated.IsPositive() {
		delta := rewardIndex.Sub(opt.LastRewardIndex)
		opt.Accumulated = opt.Accumulated.Add(opt.AmountAllocated.Mul(delta).Quo(indexPrecision))
	}
	opt.LastRewardIndex = rewardIndex
}

// appendOption stores a new option with the next id, initialized to the current index.
func (k Keeper) appendOption(ctx context.Context, opt types.DemocraticOption) (uint64, error) {
	id, err := k.DemSeq.Next(ctx)
	if err != nil {
		return 0, err
	}
	rewardIndex, err := k.demRewardIndex(ctx)
	if err != nil {
		return 0, err
	}
	opt.Id = id
	opt.AmountAllocated = math.ZeroInt()
	opt.Accumulated = math.ZeroInt()
	opt.LastRewardIndex = rewardIndex
	if err := k.DemOptions.Set(ctx, id, opt); err != nil {
		return 0, err
	}
	if opt.Kind == types.DEMOCRATIC_KIND_INTEGRATED {
		if err := k.DemIntegratedOptions.Set(ctx, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// resyncDemVoter re-applies a voter's split at the fixed one-human weight: each
// option gets exactly the percentage pointed at it (weight 100 × pct / 100 = pct),
// so total_weight = 100 × active voters. The index must already be current.
func (k Keeper) resyncDemVoter(ctx context.Context, addrBz []byte, percentages []types.DemocraticWeight) error {
	rewardIndex, err := k.demRewardIndex(ctx)
	if err != nil {
		return err
	}
	total, err := k.demTotalWeight(ctx)
	if err != nil {
		return err
	}

	epoch, err := k.demEpoch(ctx)
	if err != nil {
		return err
	}

	// Only unwind a previous vote that belongs to the current epoch. A reset
	// already zeroed the aggregates wholesale, so subtracting a stale record's
	// weight again would drive options negative.
	if old, err := k.DemVoters.Get(ctx, addrBz); err == nil && old.Epoch == epoch {
		for _, w := range old.Percentages {
			opt, err := k.DemOptions.Get(ctx, w.OptionId)
			if err != nil {
				continue
			}
			settleOption(&opt, rewardIndex)
			amt := math.NewIntFromUint64(w.Percent)
			opt.AmountAllocated = opt.AmountAllocated.Sub(amt)
			total = total.Sub(amt)
			if err := k.DemOptions.Set(ctx, w.OptionId, opt); err != nil {
				return err
			}
		}
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	for _, w := range percentages {
		opt, err := k.DemOptions.Get(ctx, w.OptionId)
		if err != nil {
			return types.ErrOptionNotFound.Wrapf("option %d", w.OptionId)
		}
		settleOption(&opt, rewardIndex)
		amt := math.NewIntFromUint64(w.Percent)
		opt.AmountAllocated = opt.AmountAllocated.Add(amt)
		total = total.Add(amt)
		if err := k.DemOptions.Set(ctx, w.OptionId, opt); err != nil {
			return err
		}
	}

	if total.IsNegative() {
		total = math.ZeroInt()
	}
	if err := k.DemTotalWeight.Set(ctx, total); err != nil {
		return err
	}

	if len(percentages) == 0 {
		return k.DemVoters.Remove(ctx, addrBz)
	}
	return k.DemVoters.Set(ctx, addrBz, types.DemocraticVoter{Percentages: percentages, Epoch: epoch})
}

// demEpoch returns the current democratic allocation epoch (0 before any reset).
func (k Keeper) demEpoch(ctx context.Context) (uint64, error) {
	v, err := k.DemEpoch.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// resetDemAllocations retires every democratic vote at once: it settles each
// option up to now, zeroes the weights, and bumps the epoch so existing voter
// records stop counting.
//
// This is O(options), not O(voters) — the voter rows are left where they are and
// simply age out of relevance. Accrued balances are deliberately preserved: a
// reset redirects the future stream, it does not confiscate ERTH an option has
// already earned.
func (k Keeper) resetDemAllocations(ctx context.Context) (uint64, error) {
	if err := k.advanceDemIndex(ctx); err != nil {
		return 0, err
	}
	rewardIndex, err := k.demRewardIndex(ctx)
	if err != nil {
		return 0, err
	}

	var ids []uint64
	if err := k.DemOptions.Walk(ctx, nil, func(id uint64, _ types.DemocraticOption) (bool, error) {
		ids = append(ids, id)
		return false, nil
	}); err != nil {
		return 0, err
	}
	for _, id := range ids {
		opt, err := k.DemOptions.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		// Settle before zeroing so everything earned up to this block is kept.
		settleOption(&opt, rewardIndex)
		opt.AmountAllocated = math.ZeroInt()
		if err := k.DemOptions.Set(ctx, id, opt); err != nil {
			return 0, err
		}
	}
	if err := k.DemTotalWeight.Set(ctx, math.ZeroInt()); err != nil {
		return 0, err
	}

	epoch, err := k.demEpoch(ctx)
	if err != nil {
		return 0, err
	}
	epoch++
	return epoch, k.DemEpoch.Set(ctx, epoch)
}

// payRegistrationReward settles option #1 and pays its accrued ERTH out on a new
// registration: 50% registree / 50% referrer (100% registree if no referrer).
// Returns the amount paid to the registree.
func (k Keeper) payRegistrationReward(ctx context.Context, registree sdk.AccAddress, referrer sdk.AccAddress) (math.Int, error) {
	opt, err := k.DemOptions.Get(ctx, types.RegistrationRewardOptionID)
	if err != nil {
		return math.ZeroInt(), nil // no registration-rewards option configured
	}
	rewardIndex, err := k.demRewardIndex(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	settleOption(&opt, rewardIndex)

	// Pay out only a fixed fraction (10 bps) of the stacked pool, so rewards are
	// normalized to the pool size and the pool decays gradually rather than being
	// fully drained by each registration.
	payout := opt.Accumulated.MulRaw(types.RegistrationRewardBps).QuoRaw(10000)
	registreeAmt := math.ZeroInt()
	if payout.IsPositive() {
		erth, err := k.erthDenom(ctx)
		if err != nil {
			return math.ZeroInt(), err
		}
		referrerAmt := math.ZeroInt()
		if referrer != nil {
			referrerAmt = payout.QuoRaw(2)
		}
		registreeAmt = payout.Sub(referrerAmt)

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
		opt.Accumulated = opt.Accumulated.Sub(payout)
	}
	if err := k.DemOptions.Set(ctx, types.RegistrationRewardOptionID, opt); err != nil {
		return math.ZeroInt(), err
	}
	return registreeAmt, nil
}
