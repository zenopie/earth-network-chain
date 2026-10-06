package keeper

import (
	"context"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/assembly/types"
)

// VoteProposal records one human's vote on an x/gov proposal, under the
// voter's nullifier for the proposal's current round. The private ante has
// verified the membership proof: a live registration, not made under a signer
// the proposal revokes, whose predecessor identity (if any) was replaced before
// the round opened (proposalInputs).
//
// The vote is not cast into x/gov. Its tally weighs bonded stake and deletes the
// votes it counts, so a human vote left there would be worth nothing and would
// not survive to be read. This module keeps its own count, and the EndBlocker
// applies it to the proposal before x/gov ever tallies.
func (k msgServer) VoteProposal(goCtx context.Context, msg *types.MsgVoteProposal) (*types.MsgVoteProposalResponse, error) {
	ctx, _, err := authorized(goCtx, msg)
	if err != nil {
		return nil, err
	}
	if _, err := k.votingProposal(ctx, msg.ProposalId); err != nil {
		return nil, err
	}

	// The proposal's current round, opened by its first vote. After an
	// expedited demotion that is a new ballot (and a new scope), so the
	// declined round's votes do not carry into the longer one.
	ballot, ok, err := k.proposalBallot(ctx, msg.ProposalId)
	if err != nil {
		return nil, err
	}
	if !ok {
		if ballot, err = k.newBallot(ctx); err != nil {
			return nil, err
		}
		if err := k.ProposalBallot.Set(ctx, msg.ProposalId, ballot); err != nil {
			return nil, err
		}
	}
	tally, err := k.recordVote(ctx, ballot, msg.Membership.Nullifier, msg.Option)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"assembly_vote",
		sdk.NewAttribute("proposal_id", strconv.FormatUint(msg.ProposalId, 10)),
		sdk.NewAttribute("option", msg.Option.String()),
		sdk.NewAttribute("yes", strconv.FormatUint(tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(tally.No, 10)),
	))
	return &types.MsgVoteProposalResponse{}, nil
}
