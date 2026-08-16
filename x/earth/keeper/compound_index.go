package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
)

// indexPrecision scales the stake compounding index to preserve fractional
// growth (matches the reward indexes in the allocation streams).
var indexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

// GetStakeCompoundIndex returns the cumulative stake growth factor, defaulting
// to 1.0. It must never be zero — stored stake weights are divided by it.
func (k Keeper) GetStakeCompoundIndex(ctx context.Context) (math.Int, error) {
	v, err := k.StakeCompoundIndex.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return indexPrecision, nil
		}
		return math.Int{}, err
	}
	if !v.IsPositive() {
		return indexPrecision, nil
	}
	return v, nil
}

// NormalizeStakeWeight converts live bonded stake into units that stay
// comparable across compounding.
//
// Auto-compounding grows every delegator's stake with no delegation event to
// observe, so anything that records a stake figure goes stale. Whoever then
// touches a delegation is resynced at the compounded value while everyone else
// stays frozen — free relative weight, repeatable by redelegating a single unit.
// Storing normalized weights removes that: consumers hold everything in the same
// units, and since they compare weights to each other, the index cancels and
// never has to be applied on the way back out.
func (k Keeper) NormalizeStakeWeight(ctx context.Context, weight math.Int) (math.Int, error) {
	idx, err := k.GetStakeCompoundIndex(ctx)
	if err != nil {
		return math.Int{}, err
	}
	return weight.Mul(indexPrecision).Quo(idx), nil
}

// AdvanceStakeCompoundIndex records that `compounded` was folded into a bonded
// pool of `totalBonded`, growing every delegator's stake by the same factor
// without any delegation event firing.
//
// `compounded` must be the amount that grew delegations *uniformly* — the
// emission net of validator commission. Commission is compounded too, but into
// the earning validator's own self-delegation, which is a real delegation that
// fires the staking hooks and is marked to market there. It is already accounted
// for and must not be counted twice.
//
// Passing the gross emission would advance the index past the growth that
// actually happened, so every consumer resynced afterwards would be normalized
// down against everyone still holding older numbers — the same asymmetry this
// index exists to remove, with the sign reversed.
//
// One approximation remains by design: commission rates differ between
// validators, so delegators do not all grow at the same rate, and a single
// global index carries only the average. The residual is bounded by the spread
// in commission rates, and far smaller than the unbounded, farmable gap that
// exists with no index at all. Making it exact would need per-validator
// tracking, which a single weight per voter cannot express.
func (k Keeper) AdvanceStakeCompoundIndex(ctx context.Context, compounded, totalBonded math.Int) error {
	if !compounded.IsPositive() || !totalBonded.IsPositive() {
		return nil
	}
	idx, err := k.GetStakeCompoundIndex(ctx)
	if err != nil {
		return err
	}
	grown := idx.Mul(totalBonded.Add(compounded)).Quo(totalBonded)
	if grown.Equal(idx) {
		return nil // too small to move the index at this precision
	}
	return k.StakeCompoundIndex.Set(ctx, grown)
}
