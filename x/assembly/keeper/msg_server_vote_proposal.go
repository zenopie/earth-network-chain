package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/assembly/types"
)

// VoteProposal records one human's vote on an x/gov proposal.
//
// The vote is not cast into x/gov. Its tally weighs bonded stake and deletes the
// votes it counts, so a human vote left there would be worth nothing and would
// not survive to be read. This module keeps its own count, and the EndBlocker
// applies it to the proposal before x/gov ever tallies.
func (k msgServer) VoteProposal(ctx context.Context, msg *types.MsgVoteProposal) (*types.MsgVoteProposalResponse, error) {
	if msg.Option != types.VOTE_OPTION_YES && msg.Option != types.VOTE_OPTION_NO {
		return nil, types.ErrBadVoteOption
	}
	nullifier, err := k.voterNullifier(ctx, msg.Voter)
	if err != nil {
		return nil, err
	}

	// Only a proposal actually open for voting accepts one. x/gov maintains this
	// set as proposals enter and leave the voting period, so asking it is asking
	// the authority rather than keeping a second copy of the same fact.
	voting, err := k.gov.VotingPeriodProposals.Has(ctx, msg.ProposalId)
	if err != nil {
		return nil, err
	}
	if !voting {
		return nil, errorsmod.Wrapf(types.ErrProposalNotVoting, "proposal %d", msg.ProposalId)
	}

	// A registration made under a signer this proposal revokes is what the
	// proposal is about, and does not vote on it. See revokedSigners.
	proposal, err := k.gov.Proposals.Get(ctx, msg.ProposalId)
	if err != nil {
		return nil, err
	}
	if subject, err := k.isSubject(ctx, revokedSigners(proposal), nullifier); err != nil {
		return nil, err
	} else if subject {
		return nil, errorsmod.Wrapf(types.ErrVoterIsSubject, "proposal %d", msg.ProposalId)
	}

	// The proposal's current round, opened by its first vote. After an
	// expedited demotion that is a new ballot, so the declined round's votes do
	// not carry into the longer one.
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
	tally, err := k.recordVote(ctx, ballot, nullifier, msg.Option)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"assembly_vote",
		sdk.NewAttribute("proposal_id", strconv.FormatUint(msg.ProposalId, 10)),
		sdk.NewAttribute("option", msg.Option.String()),
		sdk.NewAttribute("yes", strconv.FormatUint(tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(tally.No, 10)),
	))
	return &types.MsgVoteProposalResponse{}, nil
}
