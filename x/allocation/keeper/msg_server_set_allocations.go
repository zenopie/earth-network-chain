package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

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

// SetAllocations sets the sender's split across one stream's options
// (percentages must sum to 100, or be empty to clear). Groundworks only: its
// weight is the sender's bonded stake. Caretaker splits are cast anonymously
// through x/personhood's MsgSetCaretaker, which proves a live registration
// without naming it.
func (k msgServer) SetAllocations(ctx context.Context, msg *types.MsgSetAllocations) (*types.MsgSetAllocationsResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	if msg.Stream == types.STREAM_ID_CARETAKER {
		return nil, errorsmod.Wrap(types.ErrUnknownStream,
			"caretaker splits are cast privately with x/personhood MsgSetCaretaker")
	}
	addrBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	if err := k.ValidateSplit(ctx, msg.Stream, msg.Percentages); err != nil {
		return nil, err
	}

	src, err := k.weightSource(msg.Stream)
	if err != nil {
		return nil, err
	}
	weight, err := src.Weight(ctx, addrBz)
	if err != nil {
		return nil, err
	}
	// Zero weight is the stream saying "not eligible". Clearing a vote is still
	// allowed: a staker who fully unbonded must be able to tidy up.
	if len(msg.Percentages) > 0 && !weight.IsPositive() {
		return nil, types.ErrNoWeight
	}

	if err := k.AdvanceIndex(ctx, msg.Stream); err != nil {
		return nil, err
	}
	if err := k.resyncVoter(ctx, msg.Stream, addrBz, msg.Percentages, weight); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"set_allocations",
			sdk.NewAttribute("stream", msg.Stream.String()),
			sdk.NewAttribute("voter", msg.Creator),
			sdk.NewAttribute("weight", weight.String()),
		),
	)

	return &types.MsgSetAllocationsResponse{}, nil
}
