package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// SetDemocraticAllocations sets a registered human's one-human-one-vote split
// across democratic options (percentages sum to 100, or empty to clear).
func (k msgServer) SetDemocraticAllocations(ctx context.Context, msg *types.MsgSetDemocraticAllocations) (*types.MsgSetDemocraticAllocationsResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	if _, err := k.requireValidHuman(ctx, creator); err != nil {
		return nil, err
	}

	// The split is stored and later walked entry by entry to unwind it — including
	// from BeginBlock, when the expiry sweep retires a lapsed registration. Both
	// checks below exist to bound that walk: nobody pays gas for it.
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
		has, err := k.DemOptions.Has(ctx, w.OptionId)
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

	if err := k.advanceDemIndex(ctx); err != nil {
		return nil, err
	}
	if err := k.resyncDemVoter(ctx, creatorBz, msg.Percentages); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent("set_democratic_allocations", sdk.NewAttribute("voter", msg.Creator)),
	)

	return &types.MsgSetDemocraticAllocationsResponse{}, nil
}
