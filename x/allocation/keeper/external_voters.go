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
// Two modules file votes under keys that are not accounts. x/personhood files
// anonymous caretaker splits under caretaker nullifiers at a weight it decides
// (SetVoterSplit). x/shieldedstaking files all of a validator's Groundworks
// positions as one weighted voter (key "gwpos/" || validator address bytes)
// with absolute option weights it computes itself (SetWeightedVoter); a
// position's anonymous owner changes its split with a stake proof of the
// position's owner tag, never a key. These calls are those modules' way in.
// key is any byte string that cannot collide with an account address (a
// validator voter key is 26 or 38 bytes, a caretaker nullifier 32 bytes in
// its own stream; accounts are 20 or 32). MsgSetAllocations goes through ApplySplit too, so every split meets
// the same ValidateSplit.

// ValidateSplit checks a split within one stream: at most MaxVoterOptions
// entries, each a non-zero share of at most 100% of a distinct live option,
// summing to 100 (or empty, to clear).
func (k Keeper) ValidateSplit(ctx context.Context, stream types.StreamId, percentages []types.AllocationWeight) error {
	if err := ValidateStream(stream); err != nil {
		return err
	}
	// The split is stored and walked entry by entry on every resync, including
	// resyncs nobody pays gas for — the staking hooks in the capital stream, the
	// lease sweep in the caretaker one. Both checks below bound that walk.
	if len(percentages) > types.MaxVoterOptions {
		return errorsmod.Wrapf(types.ErrBadPercentages,
			"split across %d options exceeds the maximum of %d", len(percentages), types.MaxVoterOptions)
	}
	seen := make(map[uint64]struct{}, len(percentages))
	var sum uint64
	for _, w := range percentages {
		// A zero-percent entry directs nothing but still costs a read-modify-write
		// every time the split is applied or unwound.
		if w.Percent == 0 {
			return errorsmod.Wrapf(types.ErrBadPercentages, "option %d has a zero share", w.OptionId)
		}
		// Each share on its own, before the uint64 sum: two shares near 2^63
		// could otherwise sum to exactly 100 modulo 2^64 and sign-flip to a
		// negative weight in resyncVoter.
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
		// Refused rather than silently ignored: resyncVoter skips a struck
		// option when replaying a stored split, but a voter casting one now can
		// be told.
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

// SetVoterSplit validates a split, settles the stream and files the split
// under voter at weight (clearing it when percentages is empty). For a module
// that decides a stream's voters itself: x/personhood files anonymous
// caretaker splits under their caretaker nullifiers.
func (k Keeper) SetVoterSplit(ctx context.Context, stream types.StreamId, voter []byte, percentages []types.AllocationWeight, weight math.Int) error {
	if err := k.ValidateSplit(ctx, stream, percentages); err != nil {
		return err
	}
	if len(percentages) > 0 && !weight.IsPositive() {
		return types.ErrNoWeight
	}
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	return k.resyncVoter(ctx, stream, voter, percentages, weight)
}

// ApplySplit validates and applies key's split in stream at the weight the
// stream's weight source gives key, returning that weight. An empty split
// clears key's vote.
func (k Keeper) ApplySplit(ctx context.Context, stream types.StreamId, key []byte, percentages []types.AllocationWeight) (math.Int, error) {
	// Validate before resolving weight: a bad split is refused as such even
	// from a voter the stream gives no weight.
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
	// Zero weight is the stream saying "not eligible". Clearing is still
	// allowed: a staker who fully unbonded must be able to tidy up.
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
	// A weighted voter's option weights are its module's to set
	// (SetWeightedVoter); the stream's weight source has nothing to add.
	if len(voter.OptionWeights) > 0 {
		return nil
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

// SetWeightedVoter files key in stream as a weighted voter: an absolute
// weight on each option (zero entries dropped) instead of a split at one
// weight, replacing whatever key carried before. No weights clears key's
// vote. For a module that aggregates many splits into one voter:
// x/shieldedstaking files all the Groundworks positions of a validator as one
// voter, so its epoch work grows with validators, not positions.
//
// Like a replayed split, a weight on a struck or vanished option is skipped
// rather than refused (the module re-files its totals every epoch, with no
// one to tell). A duplicate option or a negative weight is the caller's bug
// and is refused.
func (k Keeper) SetWeightedVoter(ctx context.Context, stream types.StreamId, key []byte, weights []types.OptionWeight) error {
	if err := ValidateStream(stream); err != nil {
		return err
	}
	ws := make([]types.OptionWeight, 0, len(weights))
	seen := make(map[uint64]struct{}, len(weights))
	sum := math.ZeroInt()
	for _, w := range weights {
		if w.Weight.IsNil() || w.Weight.IsNegative() {
			return errorsmod.Wrapf(types.ErrBadPercentages, "option %d has a negative weight", w.OptionId)
		}
		if _, dup := seen[w.OptionId]; dup {
			return errorsmod.Wrapf(types.ErrBadPercentages, "duplicate option %d", w.OptionId)
		}
		seen[w.OptionId] = struct{}{}
		if w.Weight.IsZero() {
			continue
		}
		ws = append(ws, w)
		sum = sum.Add(w.Weight)
	}
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	add := make([]contribution, len(ws))
	for i, w := range ws {
		add[i] = contribution{w.OptionId, w.Weight}
	}
	return k.writeVoter(ctx, stream, key, types.Voter{OptionWeights: ws, Weight: sum}, add, len(ws) > 0)
}

// StreamEpoch is stream's allocation epoch: 0 until governance first resets
// it (ResetAllocations), then bumped by each reset. A split cast in an older
// epoch no longer counts.
func (k Keeper) StreamEpoch(ctx context.Context, stream types.StreamId) (uint64, error) {
	return k.getEpoch(ctx, stream)
}

// MoveVoter files from's split (percentages and weight) under to and clears
// from, settling the stream first. For x/personhood's caretaker move: an
// identity switch hands its live split to the new identity's nullifier. A
// from with no live split (none, or from an older epoch) moves nothing.
func (k Keeper) MoveVoter(ctx context.Context, stream types.StreamId, from, to []byte) error {
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	old, err := k.Voters.Get(ctx, voterKey(stream, from))
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return err
	}
	if err := k.resyncVoter(ctx, stream, from, nil, math.ZeroInt()); err != nil {
		return err
	}
	if old.Epoch != epoch {
		return nil
	}
	return k.resyncVoter(ctx, stream, to, old.Percentages, old.Weight)
}
