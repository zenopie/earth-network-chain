package keeper

import (
	"context"
	"errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Groundworks positions. A position is derth locked in this module with a
// public split and an anonymous owner. It weighs derth x rate_v in the
// Groundworks stream, at the rate of the last epoch end, so every position of
// a validator moves together once an epoch and not with every block's
// rewards.
//
// Positions are weighed per validator, not one by one. For each validator v
// and option o the module keeps T[v][o] = sum over v's live positions of
// derth x percent (exact integers: nothing is divided until the weight is
// taken), adjusted by Lock, Update and Unlock in O(options). All of v's
// positions are one Groundworks voter (types.ValidatorVoterKey) with an
// absolute weight on each option, trunc(rate_v x T[v][o] / 100)
// (allocation.SetWeightedVoter). An epoch end or a slash re-files one voter
// per validator: the work grows with validators, never with positions, so
// positions need no cap.
//
// A governance reset of the stream (ResetAllocations) bumps its epoch. A
// position's split counts only in the epoch it was cast in (split_epoch), and
// a validator's totals only in the epoch recorded for them (GwEpoch): after a
// reset both are stale, treated as zero, and the owner votes again with
// MsgUpdatePosition, as any voter does after a reset.

func (k Keeper) setPosition(ctx context.Context, p types.Position) error {
	p.Weight = math.ZeroInt() // not stored: withLiveWeight
	return k.Positions.Set(ctx, p.Id, p)
}

// gwEpoch is the Groundworks stream's allocation epoch.
func (k Keeper) gwEpoch(ctx context.Context) (uint64, error) {
	return k.allocation.StreamEpoch(ctx, allocationtypes.STREAM_ID_GROUNDWORKS)
}

// positionLive reports whether p's split counts in Groundworks epoch epoch.
func positionLive(p types.Position, epoch uint64) bool {
	return len(p.Splits) > 0 && p.SplitEpoch == epoch
}

// withLiveWeight is p with Weight filled in: derth x its validator's epoch
// rate while its split counts, else zero (what queries and events show).
func (k Keeper) withLiveWeight(ctx context.Context, p types.Position) types.Position {
	p.Weight = math.ZeroInt()
	if epoch, err := k.gwEpoch(ctx); err == nil && positionLive(p, epoch) {
		p.Weight = k.positionWeight(ctx, p.Validator, p.Derth)
	}
	return p
}

// freshTotals makes v's totals belong to epoch: totals from an older epoch
// (before a reset) are deleted first.
func (k Keeper) freshTotals(ctx context.Context, v string, epoch uint64) error {
	e, err := k.GwEpoch.Get(ctx, v)
	if err == nil && e == epoch {
		return nil
	}
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.GwTotals.Clear(ctx, collections.NewPrefixedPairRange[string, uint64](v)); err != nil {
		return err
	}
	return k.GwEpoch.Set(ctx, v, epoch)
}

// addPositionTotals adds (sign 1) or removes (sign -1) a live position's
// contribution, derth x percent per option, to its validator's totals.
// Removing exactly what adding put there is the point: the same position
// always contributes the same integers.
func (k Keeper) addPositionTotals(ctx context.Context, p types.Position, sign int64, epoch uint64) error {
	if err := k.freshTotals(ctx, p.Validator, epoch); err != nil {
		return err
	}
	for _, w := range p.Splits {
		key := collections.Join(p.Validator, w.OptionId)
		cur, err := k.GwTotals.Get(ctx, key)
		if errors.Is(err, collections.ErrNotFound) {
			cur = math.ZeroInt()
		} else if err != nil {
			return err
		}
		next := cur.Add(p.Derth.MulRaw(int64(w.Percent)).MulRaw(sign))
		switch {
		case next.IsNegative():
			return types.ErrInvariant.Wrapf("groundworks total %s/%d below zero", p.Validator, w.OptionId)
		case next.IsZero():
			err = k.GwTotals.Remove(ctx, key)
		default:
			err = k.GwTotals.Set(ctx, key, next)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// validatorOptionWeights is v's Groundworks voter: trunc(rate_v x T[v][o] /
// 100) per option, nothing when its totals are stale.
func (k Keeper) validatorOptionWeights(ctx context.Context, v string, epoch uint64) ([]allocationtypes.OptionWeight, error) {
	e, err := k.GwEpoch.Get(ctx, v)
	if errors.Is(err, collections.ErrNotFound) || (err == nil && e != epoch) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rate := k.epochRate(ctx, v)
	var ws []allocationtypes.OptionWeight
	err = k.GwTotals.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](v),
		func(key collections.Pair[string, uint64], t math.Int) (bool, error) {
			w := rate.MulInt(t).QuoInt64(100).TruncateInt()
			if w.IsPositive() {
				ws = append(ws, allocationtypes.OptionWeight{OptionId: key.K2(), Weight: w})
			}
			return false, nil
		})
	return ws, err
}

// syncValidatorVoter re-files v's Groundworks voter from its totals at its
// epoch rate; stale totals (a reset since) are deleted and the voter cleared.
func (k Keeper) syncValidatorVoter(ctx context.Context, v string) error {
	valBz, err := k.valAddr(v)
	if err != nil {
		return err
	}
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	if e, err := k.GwEpoch.Get(ctx, v); err == nil && e != epoch {
		if err := k.GwTotals.Clear(ctx, collections.NewPrefixedPairRange[string, uint64](v)); err != nil {
			return err
		}
		if err := k.GwEpoch.Remove(ctx, v); err != nil {
			return err
		}
	}
	// No live totals left (the last position unlocked or cleared its
	// split): the validator leaves the Groundworks index, so nothing walks
	// or re-files it until a position votes again.
	empty := true
	if err := k.GwTotals.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](v),
		func(collections.Pair[string, uint64], math.Int) (bool, error) { empty = false; return true, nil }); err != nil {
		return err
	}
	if empty {
		if err := k.GwEpoch.Remove(ctx, v); err != nil {
			return err
		}
	}
	ws, err := k.validatorOptionWeights(ctx, v, epoch)
	if err != nil {
		return err
	}
	return k.allocation.SetWeightedVoter(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.ValidatorVoterKey(valBz), ws)
}

// resyncValidatorVoter is syncValidatorVoter in its own cache, for EndBlock:
// on failure v keeps its old weights until the next try.
func (k Keeper) resyncValidatorVoter(ctx context.Context, v string) {
	if err := k.guarded(ctx, func(cc context.Context) error { return k.syncValidatorVoter(cc, v) }); err != nil {
		k.failure(ctx, "reweigh_positions", v, err)
	}
}

// ReweighGroundworks re-files every validator's Groundworks voter at its
// epoch rate: one voter per validator with live positions, whatever the
// number of positions. Unbounded in validators, so the epoch end does not
// run it (the bounded book sweep re-files each voter, resyncBooks); for
// tests and tools. Never fails.
func (k Keeper) ReweighGroundworks(ctx context.Context) {
	var vals []string
	_ = k.GwEpoch.Walk(ctx, nil, func(v string, _ uint64) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
	for _, v := range vals {
		k.resyncValidatorVoter(ctx, v)
	}
}

// applyPositionSplit moves p (as stored, old) to splits: its old live
// contribution comes off its validator's totals, the new one goes on in the
// current epoch, and the validator's voter is re-filed. It returns the
// position to store.
func (k Keeper) applyPositionSplit(ctx context.Context, old types.Position, splits []allocationtypes.AllocationWeight, existed bool) (types.Position, error) {
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return old, err
	}
	if existed && positionLive(old, epoch) {
		if err := k.addPositionTotals(ctx, old, -1, epoch); err != nil {
			return old, err
		}
	}
	p := old
	p.Splits, p.SplitEpoch = splits, epoch
	if len(splits) == 0 {
		p.Splits, p.SplitEpoch = nil, 0
	} else if err := k.addPositionTotals(ctx, p, 1, epoch); err != nil {
		return old, err
	}
	if existed && positionLive(old, epoch) || len(splits) > 0 {
		if err := k.syncValidatorVoter(ctx, p.Validator); err != nil {
			return old, err
		}
	}
	return p, nil
}

// epochRate is v's rate at the last epoch end, 1 before its first.
func (k Keeper) epochRate(ctx context.Context, valoper string) math.LegacyDec {
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil || vs.EpochRate.IsNil() || !vs.EpochRate.IsPositive() {
		return math.LegacyOneDec()
	}
	return vs.EpochRate
}

// positionWeight is derth x epoch rate, truncated.
func (k Keeper) positionWeight(ctx context.Context, valoper string, derth math.Int) math.Int {
	return k.epochRate(ctx, valoper).MulInt(derth).TruncateInt()
}

// PositionWeightSource is the Groundworks stream's weight source:
//
//   - a validator's positions voter (types.ValidatorVoterKey): zero here;
//     this module files its option weights itself (SetWeightedVoter);
//   - this module's own account: zero. Its delegations are the private stake,
//     already counted through the positions; counting them again as an
//     account's bond would weigh that stake twice;
//   - any other account: its bonded stake. Transparent delegation is refused
//     except a validator operator's self-bond, so that is all it can be: a
//     validator's self-bond votes like any stake.
type PositionWeightSource struct{ k Keeper }

// NewPositionWeightSource returns the source to register with x/allocation.
func NewPositionWeightSource(k Keeper) PositionWeightSource { return PositionWeightSource{k: k} }

var (
	_ allocationtypes.WeightSource  = PositionWeightSource{}
	_ allocationtypes.BondedTracker = PositionWeightSource{}
)

func (s PositionWeightSource) Weight(ctx context.Context, key []byte) (math.Int, error) {
	if !s.TracksBonded(key) {
		return math.ZeroInt(), nil
	}
	return s.k.staking.GetDelegatorBonded(ctx, sdk.AccAddress(key))
}

// TracksBonded: x/allocation's staking hooks resync an account voter's weight
// from its bonded stake when a delegation changes. Not for a validator's
// positions voter (this module files its option weights itself,
// SetWeightedVoter) and never for this module's account, whose delegations
// change every epoch and carry no weight.
func (s PositionWeightSource) TracksBonded(key []byte) bool {
	if types.IsValidatorVoterKey(key) {
		return false
	}
	return !sdk.AccAddress(key).Equals(s.k.modAddr)
}

// reweighSlashed: a slash lowers a validator's rate at once (its delegation
// lost tokens), so its positions must not keep voting at the pre-slash epoch
// rate until the next epoch. For each validator slashed this block (recorded
// by the slash hook, which runs before the tokens move): the live rate, after
// the slash, becomes its epoch rate, and only its positions re-weigh at it,
// each in its own cache. Rewards still reach positions at the daily epoch.
// Never fails: it runs in EndBlock.
func (k Keeper) reweighSlashed(ctx context.Context) {
	var vals []string
	_ = k.SlashedValidators.Walk(ctx, nil, func(v string) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
	for _, v := range vals {
		err := k.guarded(ctx, func(cc context.Context) error {
			rate, err := k.Rate(cc, v)
			if err != nil {
				return err
			}
			vs, err := k.ValidatorState(cc, v)
			if err != nil {
				return err
			}
			vs.EpochRate = rate
			return k.Validators.Set(cc, v, vs)
		})
		if err != nil {
			k.failure(ctx, "slash_rate", v, err)
		} else if _, err := k.GwEpoch.Get(ctx, v); err == nil {
			k.resyncValidatorVoter(ctx, v)
		}
		_ = k.SlashedValidators.Remove(ctx, v)
	}
}

// gwKey is a (validator, option) totals key as a comparable map key.
type gwKey struct {
	val    string
	option uint64
}

// gwTotalsOf computes every validator's totals from the positions: the sum
// of derth x percent over its positions whose split counts in epoch. O(positions):
// for InitGenesis and the invariant only.
func (k Keeper) gwTotalsOf(ctx context.Context, epoch uint64) (map[gwKey]math.Int, error) {
	out := map[gwKey]math.Int{}
	err := k.Positions.Walk(ctx, nil, func(_ uint64, p types.Position) (bool, error) {
		if !positionLive(p, epoch) {
			return false, nil
		}
		for _, w := range p.Splits {
			key := gwKey{p.Validator, w.OptionId}
			c := p.Derth.MulRaw(int64(w.Percent))
			if cur, ok := out[key]; ok {
				c = c.Add(cur)
			}
			out[key] = c
		}
		return false, nil
	})
	return out, err
}

// rebuildGwTotals writes the totals from the imported positions
// (InitGenesis; x/allocation, initialized first, already holds the voters).
func (k Keeper) rebuildGwTotals(ctx context.Context) error {
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	totals, err := k.gwTotalsOf(ctx, epoch)
	if err != nil {
		return err
	}
	for key, t := range totals {
		if err := k.GwTotals.Set(ctx, collections.Join(key.val, key.option), t); err != nil {
			return err
		}
		if err := k.GwEpoch.Set(ctx, key.val, epoch); err != nil {
			return err
		}
	}
	return nil
}

// assertGwTotals: every validator's stored totals (in the current epoch)
// equal the sum of its live positions' contributions.
func (k Keeper) assertGwTotals(ctx context.Context) error {
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	want, err := k.gwTotalsOf(ctx, epoch)
	if err != nil {
		return err
	}
	seen := 0
	err = k.GwTotals.Walk(ctx, nil, func(key collections.Pair[string, uint64], t math.Int) (bool, error) {
		e, err := k.GwEpoch.Get(ctx, key.K1())
		if err != nil || e != epoch {
			return false, nil // stale: counts as zero, deleted on next touch
		}
		w, ok := want[gwKey{key.K1(), key.K2()}]
		if !ok || !w.Equal(t) {
			return true, types.ErrInvariant.Wrapf("groundworks total %s/%d is %s, positions give %s", key.K1(), key.K2(), t, w)
		}
		seen++
		return false, nil
	})
	if err != nil {
		return err
	}
	if seen != len(want) {
		return types.ErrInvariant.Wrapf("%d groundworks totals stored, positions give %d", seen, len(want))
	}
	return nil
}
