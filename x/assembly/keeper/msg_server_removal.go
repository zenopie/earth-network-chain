package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/assembly/types"
)

// ProposeRemoval opens a ballot to remove a live groundworks allocation option.
//
// This is the chamber's one affirmative power. Everything else it does is a
// refusal, which is what makes its reach over the whole of governance
// affordable: a body that can only say no cannot direct money to itself. Removal
// is the exception, and it only ever subtracts — it takes an option off the
// slate and can put nothing in its place.
//
// Stake has no say. A groundworks option is paid by a stake-weighted vote, so
// letting stake veto its removal would leave the humans able to object to a
// capture and unable to end one.
func (k msgServer) ProposeRemoval(ctx context.Context, msg *types.MsgProposeRemoval) (*types.MsgProposeRemovalResponse, error) {
	if _, err := k.voterNullifier(ctx, msg.Proposer); err != nil {
		return nil, err
	}

	removable, err := k.allocation.GroundworksOptionRemovable(ctx, msg.OptionId)
	if err != nil {
		return nil, err
	}
	if !removable {
		return nil, errorsmod.Wrapf(types.ErrNotRemovable, "option %d", msg.OptionId)
	}

	// One ballot per option at a time. Without this, an option could be kept
	// under a permanent rolling vote by anyone willing to reopen it, which is
	// harassment rather than deliberation — and the tally that matters would be
	// whichever one happened to be open.
	if has, err := k.RemovalBallots.Has(ctx, msg.OptionId); err != nil {
		return nil, err
	} else if has {
		return nil, errorsmod.Wrapf(types.ErrBallotExists, "option %d", msg.OptionId)
	}

	closesAt := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + types.RemovalVotingPeriod
	ballot := types.RemovalBallot{
		OptionId: msg.OptionId,
		Proposer: msg.Proposer,
		ClosesAt: closesAt,
		Tally:    types.Tally{},
	}
	if err := k.RemovalBallots.Set(ctx, msg.OptionId, ballot); err != nil {
		return nil, err
	}
	if err := k.RemovalQueue.Set(ctx, collections.Join(closesAt, msg.OptionId)); err != nil {
		return nil, err
	}
	// Its own ballot, so a later ballot on the same option starts from nothing
	// even while this one's votes are still being cleared.
	id, err := k.newBallot(ctx)
	if err != nil {
		return nil, err
	}
	if err := k.RemovalBallotID.Set(ctx, msg.OptionId, id); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"assembly_removal_opened",
		sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
		sdk.NewAttribute("closes_at", strconv.FormatInt(closesAt, 10)),
	))
	return &types.MsgProposeRemovalResponse{ClosesAt: closesAt}, nil
}

// VoteRemoval records one human's vote on an open removal ballot.
func (k msgServer) VoteRemoval(ctx context.Context, msg *types.MsgVoteRemoval) (*types.MsgVoteRemovalResponse, error) {
	if msg.Option != types.VOTE_OPTION_YES && msg.Option != types.VOTE_OPTION_NO {
		return nil, types.ErrBadVoteOption
	}
	nullifier, err := k.voterNullifier(ctx, msg.Voter)
	if err != nil {
		return nil, err
	}

	ballot, err := k.RemovalBallots.Get(ctx, msg.OptionId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, errorsmod.Wrapf(types.ErrBallotNotFound, "option %d", msg.OptionId)
		}
		return nil, err
	}

	id, err := k.RemovalBallotID.Get(ctx, msg.OptionId)
	if err != nil {
		return nil, err
	}
	ballot.Tally, err = k.recordVote(ctx, id, nullifier, msg.Option)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"assembly_removal_vote",
		sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
		sdk.NewAttribute("option", msg.Option.String()),
		sdk.NewAttribute("yes", strconv.FormatUint(ballot.Tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(ballot.Tally.No, 10)),
	))
	return &types.MsgVoteRemovalResponse{}, nil
}
