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
// public split and an anonymous owner (a one-time key). It weighs derth x
// rate_v in the Groundworks stream, at the rate of the last epoch end, so every
// position of a validator moves together once an epoch and not with every
// block's rewards.

func (k Keeper) setPosition(ctx context.Context, p types.Position) error {
	return k.Positions.Set(ctx, p.Id, p)
}

func (k Keeper) positionCount(ctx context.Context) (uint64, error) {
	var n uint64
	err := k.Positions.Walk(ctx, nil, func(uint64, types.Position) (bool, error) {
		n++
		return false, nil
	})
	return n, err
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
//   - a position's voter key: the position's weight (derth x the validator's
//     epoch rate), the private stake;
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
	id, ok := types.ParsePositionVoterKey(key)
	if !ok {
		if !s.TracksBonded(key) {
			return math.ZeroInt(), nil
		}
		return s.k.staking.GetDelegatorBonded(ctx, sdk.AccAddress(key))
	}
	p, err := s.k.Positions.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return math.ZeroInt(), nil
	} else if err != nil {
		return math.Int{}, err
	}
	return s.k.positionWeight(ctx, p.Validator, p.Derth), nil
}

// TracksBonded: x/allocation's staking hooks resync an account voter's weight
// from its bonded stake when a delegation changes. Not for a position (no
// delegation of its own; reweighPositions moves it each epoch) and never for
// this module's account, whose delegations change every epoch and carry no
// weight.
func (s PositionWeightSource) TracksBonded(key []byte) bool {
	if _, ok := types.ParsePositionVoterKey(key); ok {
		return false
	}
	return !sdk.AccAddress(key).Equals(s.k.modAddr)
}

// reweighPositions re-applies every position's split at its validator's new
// epoch rate. Each in its own cache: a position that fails keeps its old
// weight until the next epoch.
func (k Keeper) reweighPositions(ctx context.Context) {
	var ids []uint64
	_ = k.Positions.Walk(ctx, nil, func(id uint64, _ types.Position) (bool, error) {
		ids = append(ids, id)
		return false, nil
	})
	for _, id := range ids {
		k.reweighPosition(ctx, id)
	}
}

// reweighPosition re-applies one position's split at its validator's epoch
// rate, in its own cache: on failure it keeps its old weight until the next
// try.
func (k Keeper) reweighPosition(ctx context.Context, id uint64) {
	err := k.guarded(ctx, func(cc context.Context) error {
		if err := k.allocation.ResyncVoter(cc, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(id)); err != nil {
			return err
		}
		p, err := k.Positions.Get(cc, id)
		if err != nil {
			return err
		}
		p.Weight = k.positionWeight(cc, p.Validator, p.Derth)
		if len(p.Splits) == 0 {
			p.Weight = math.ZeroInt()
		}
		return k.setPosition(cc, p)
	})
	if err != nil {
		k.failure(ctx, "reweigh_position", "", err)
	}
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
		} else {
			var ids []uint64
			_ = k.PositionsByVal.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](v),
				func(key collections.Pair[string, uint64]) (bool, error) {
					ids = append(ids, key.K2())
					return false, nil
				})
			for _, id := range ids {
				k.reweighPosition(ctx, id)
			}
		}
		_ = k.SlashedValidators.Remove(ctx, v)
	}
}
