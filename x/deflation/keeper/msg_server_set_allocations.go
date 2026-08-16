package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/deflation/types"
)

// SetAllocations sets the sender's stake-weighted split across allocation
// options (percentages must sum to 100, or be empty to clear). The sender's
// bonded stake is the weight applied.
func (k msgServer) SetAllocations(ctx context.Context, msg *types.MsgSetAllocations) (*types.MsgSetAllocationsResponse, error) {
	addrBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}

	// The split is stored and walked entry by entry on every resync, including
	// resyncs the staking hooks trigger rather than the voter. Both checks below
	// bound that walk.
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
		has, err := k.AllocationOptions.Has(ctx, w.OptionId)
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

	if err := k.advanceAllocationIndex(ctx); err != nil {
		return nil, err
	}

	weight, err := k.stakingKeeper.GetDelegatorBonded(ctx, sdk.AccAddress(addrBz))
	if err != nil {
		return nil, err
	}
	if len(msg.Percentages) > 0 && !weight.IsPositive() {
		return nil, types.ErrNoStake
	}

	if err := k.resyncVoter(ctx, addrBz, msg.Percentages, weight); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"set_allocations",
			sdk.NewAttribute("voter", msg.Creator),
			sdk.NewAttribute("weight", weight.String()),
		),
	)

	return &types.MsgSetAllocationsResponse{}, nil
}
