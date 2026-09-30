package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/allocation/types"
)

// Voters that no account signs for.
//
// x/shieldedstaking's Groundworks positions vote in the Groundworks stream,
// but a position is not an account: its owner is anonymous, its split is
// changed by a msg signed with the position's own key inside an unsigned tx,
// and its weight (derth x rate) is resolved by the weight source that module
// registers for the stream. These calls are that module's way in. key is any
// byte string that cannot collide with an account address (a position key is
// 17 bytes; accounts are 20 or 32).

// ValidateSplit checks a split against one stream's options exactly as
// MsgSetAllocations does: at most MaxVoterOptions entries, each 1..100%, no
// duplicates, every option existing and not struck, summing to 100 (or empty,
// which clears).
func (k Keeper) ValidateSplit(ctx context.Context, stream types.StreamId, percentages []types.AllocationWeight) error {
	if err := ValidateStream(stream); err != nil {
		return err
	}
	if len(percentages) > types.MaxVoterOptions {
		return errorsmod.Wrapf(types.ErrBadPercentages,
			"split across %d options exceeds the maximum of %d", len(percentages), types.MaxVoterOptions)
	}
	seen := make(map[uint64]struct{}, len(percentages))
	var sum uint64
	for _, w := range percentages {
		if w.Percent == 0 {
			return errorsmod.Wrapf(types.ErrBadPercentages, "option %d has a zero share", w.OptionId)
		}
		if w.Percent > 100 {
			return errorsmod.Wrapf(types.ErrBadPercentages, "option %d has a share of %d%%, over 100", w.OptionId, w.Percent)
		}
		if _, dup := seen[w.OptionId]; dup {
			return errorsmod.Wrapf(types.ErrBadPercentages, "duplicate option %d", w.OptionId)
		}
		seen[w.OptionId] = struct{}{}
		opt, err := k.Options.Get(ctx, optionKey(stream, w.OptionId))
		if err != nil {
			if errors.Is(err, collections.ErrNotFound) {
				return errorsmod.Wrapf(types.ErrOptionNotFound, "option %d", w.OptionId)
			}
			return err
		}
		if opt.Removed {
			return errorsmod.Wrapf(types.ErrOptionRemoved, "option %d", w.OptionId)
		}
		sum += w.Percent
	}
	if len(percentages) > 0 && sum != 100 {
		return errorsmod.Wrapf(types.ErrBadPercentages, "got %d%%", sum)
	}
	return nil
}

// ApplySplit validates and applies key's split in stream at the weight the
// stream's weight source gives key, returning that weight. An empty split
// clears key's vote.
func (k Keeper) ApplySplit(ctx context.Context, stream types.StreamId, key []byte, percentages []types.AllocationWeight) (math.Int, error) {
	if err := k.ValidateSplit(ctx, stream, percentages); err != nil {
		return math.Int{}, err
	}
	src, err := k.weightSource(stream)
	if err != nil {
		return math.Int{}, err
	}
	weight, err := src.Weight(ctx, key)
	if err != nil {
		return math.Int{}, err
	}
	if len(percentages) > 0 && !weight.IsPositive() {
		return math.Int{}, types.ErrNoWeight
	}
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return math.Int{}, err
	}
	if err := k.resyncVoter(ctx, stream, key, percentages, weight); err != nil {
		return math.Int{}, err
	}
	return weight, nil
}

// ResyncVoter re-applies key's stored split at the weight the stream's source
// gives it now. A no-op for a key that has not voted; a split cast before the
// stream's last reset is dropped rather than re-applied (see
// resyncFromBonded). Never refuses a split for naming a struck or pruned
// option: resyncVoter skips those.
func (k Keeper) ResyncVoter(ctx context.Context, stream types.StreamId, key []byte) error {
	voter, err := k.Voters.Get(ctx, voterKey(stream, key))
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return err
	}
	if voter.Epoch != epoch {
		return k.Voters.Remove(ctx, voterKey(stream, key))
	}
	src, err := k.weightSource(stream)
	if err != nil {
		return err
	}
	weight, err := src.Weight(ctx, key)
	if err != nil {
		return err
	}
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	return k.resyncVoter(ctx, stream, key, voter.Percentages, weight)
}

// RemoveVoter clears key's vote in stream, returning its weight to the
// stream, after settling the stream's index.
func (k Keeper) RemoveVoter(ctx context.Context, stream types.StreamId, key []byte) error {
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	return k.ClearVoter(ctx, stream, key)
}
