package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/assembly/types"
)

// ProposeRemoval opens a ballot to remove a live groundworks allocation option.
//
// This is the chamber's one affirmative power. Everything else it does is a
// refusal, which is what makes its reach over the whole of governance
// affordable: a body that can only say no cannot direct money to itself.
// Removal is the exception, and it only ever subtracts.
//
// Stake has no say. A groundworks option is paid by a stake-weighted vote, so
// letting stake veto its removal would leave the humans able to object to a
// capture and unable to end one. The proposer is anonymous: the membership
// proof shows a live registration opened it, and nothing records which.
func (k msgServer) ProposeRemoval(goCtx context.Context, msg *types.MsgProposeRemoval) (*types.MsgProposeRemovalResponse, error) {
	ctx, _, err := authorized(goCtx, msg)
	if err != nil {
		return nil, err
	}
	if err := k.checkProposeRemoval(ctx, msg.OptionId); err != nil {
		return nil, err
	}

	now := ctx.BlockTime().Unix()
	closesAt := now + types.RemovalVotingPeriod
	// Its own ballot, so a later ballot on the same option starts from nothing
	// even while this one's votes are still being cleared. The id is part of
	// the ballot's scope.
	id, err := k.newBallot(ctx)
	if err != nil {
		return nil, err
	}
	if err := k.RemovalBallots.Set(ctx, msg.OptionId, types.RemovalBallot{
		OptionId: msg.OptionId,
		ClosesAt: closesAt,
		OpenedAt: now,
		BallotId: id,
	}); err != nil {
		return nil, err
	}
	if err := k.RemovalQueue.Set(ctx, collections.Join(closesAt, msg.OptionId)); err != nil {
		return nil, err
	}
	if err := k.RemovalBallotID.Set(ctx, msg.OptionId, id); err != nil {
		return nil, err
	}
	// Past its cooldown (checkProposeRemoval): the entry has done its work.
	if err := k.RemovalCooldown.Remove(ctx, msg.OptionId); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"assembly_removal_opened",
		sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
		sdk.NewAttribute("ballot_id", strconv.FormatUint(id, 10)),
		sdk.NewAttribute("closes_at", strconv.FormatInt(closesAt, 10)),
	))
	return &types.MsgProposeRemovalResponse{ClosesAt: closesAt, BallotId: id}, nil
}

// VoteRemoval records one human's vote on an open removal ballot, under the
// voter's nullifier for the ballot's scope.
func (k msgServer) VoteRemoval(goCtx context.Context, msg *types.MsgVoteRemoval) (*types.MsgVoteRemovalResponse, error) {
	ctx, _, err := authorized(goCtx, msg)
	if err != nil {
		return nil, err
	}
	_, ballot, err := k.removalInputs(ctx, msg.OptionId)
	if err != nil {
		return nil, err
	}
	ballot.Tally, err = k.recordVote(ctx, ballot.BallotId, msg.Membership.Nullifier, msg.Option)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"assembly_removal_vote",
		sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
		sdk.NewAttribute("option", msg.Option.String()),
		sdk.NewAttribute("yes", strconv.FormatUint(ballot.Tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(ballot.Tally.No, 10)),
	))
	return &types.MsgVoteRemovalResponse{}, nil
}
