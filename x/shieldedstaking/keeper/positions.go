package keeper

import (
	"context"
	"errors"

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

// PositionWeightSource is the Groundworks stream's weight source: a position's
// weight for its voter key, zero for anything else. It replaces the stream's
// bonded-stake source: with the module the only delegator, an account's
// bonded stake says nothing about who staked.
type PositionWeightSource struct{ k Keeper }

// NewPositionWeightSource returns the source to register with x/allocation.
func NewPositionWeightSource(k Keeper) PositionWeightSource { return PositionWeightSource{k: k} }

var _ allocationtypes.WeightSource = PositionWeightSource{}

func (s PositionWeightSource) Weight(ctx context.Context, key []byte) (math.Int, error) {
	id, ok := types.ParsePositionVoterKey(key)
	if !ok {
		return math.ZeroInt(), nil
	}
	p, err := s.k.Positions.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return math.ZeroInt(), nil
	} else if err != nil {
		return math.Int{}, err
	}
	return s.k.positionWeight(ctx, p.Validator, p.Derth), nil
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
}
