package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// SetAllocations sets the sender's split across one stream's options
// (percentages must sum to 100, or be empty to clear). Who may vote and with how
// much weight is the stream's business: a live registration in the human stream,
// bonded stake in the capital stream. Everything else is identical.
func (k msgServer) SetAllocations(ctx context.Context, msg *types.MsgSetAllocations) (*types.MsgSetAllocationsResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	addrBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}

	// The split is stored and walked entry by entry on every resync, including
	// resyncs nobody pays gas for — the staking hooks in the capital stream, the
	// expiry sweep in the human one. Both checks below bound that walk.
	if len(msg.Percentages) > types.MaxVoterOptions {
		return nil, errorsmod.Wrapf(types.ErrBadPercentages,
			"split across %d options exceeds the maximum of %d", len(msg.Percentages), types.MaxVoterOptions)
	}

	seen := make(map[uint64]struct{}, len(msg.Percentages))
	var sum uint64
	for _, w := range msg.Percentages {
		// A zero-percent entry directs nothing but still costs a read-modify-write
		// every time the split is applied or unwound.
		if w.Percent == 0 {
			return nil, errorsmod.Wrapf(types.ErrBadPercentages, "option %d has a zero share", w.OptionId)
		}
		if _, dup := seen[w.OptionId]; dup {
			return nil, errorsmod.Wrapf(types.ErrBadPercentages, "duplicate option %d", w.OptionId)
		}
		seen[w.OptionId] = struct{}{}
		has, err := k.Options.Has(ctx, optionKey(msg.Stream, w.OptionId))
		if err != nil {
			return nil, err
		}
		if !has {
			return nil, errorsmod.Wrapf(types.ErrOptionNotFound, "option %d", w.OptionId)
		}
		sum += w.Percent
	}
	if len(msg.Percentages) > 0 && sum != 100 {
		return nil, errorsmod.Wrapf(types.ErrBadPercentages, "got %d%%", sum)
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
	// allowed: a human whose registration lapsed, or a staker who fully unbonded,
	// must be able to tidy up after themselves.
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
