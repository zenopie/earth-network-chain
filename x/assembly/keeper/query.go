package keeper

import (
	"context"

	"github.com/earth-network/earth/x/assembly/types"
)

type queryServer struct{ k Keeper }

// NewQueryServerImpl returns an implementation of the QueryServer interface.
func NewQueryServerImpl(k Keeper) types.QueryServer { return queryServer{k: k} }

var _ types.QueryServer = queryServer{}

// ProposalTally reports the human vote on a proposal, and whether it clears the
// bar as it stands. A proposal with no votes reports approved=false, which is
// the answer that matters most: silence does not carry here.
func (q queryServer) ProposalTally(ctx context.Context, req *types.QueryProposalTallyRequest) (*types.QueryProposalTallyResponse, error) {
	tally, err := q.k.proposalTally(ctx, req.ProposalId)
	if err != nil {
		return nil, err
	}
	return &types.QueryProposalTallyResponse{
		Tally:    tally,
		Approved: types.Approves(tally.Yes, tally.No),
	}, nil
}

// RemovalBallots lists the open removal ballots. Unpaginated: only one ballot
// per option can be open at a time and only groundworks options are removable,
// so the list is bounded by a slate that governance itself has to admit to.
func (q queryServer) RemovalBallots(ctx context.Context, _ *types.QueryRemovalBallotsRequest) (*types.QueryRemovalBallotsResponse, error) {
	var ballots []types.RemovalBallot
	if err := q.k.RemovalBallots.Walk(ctx, nil, func(optionID uint64, b types.RemovalBallot) (bool, error) {
		tally, err := q.k.removalTally(ctx, optionID)
		if err != nil {
			return true, err
		}
		b.Tally = tally
		ballots = append(ballots, b)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return &types.QueryRemovalBallotsResponse{Ballots: ballots}, nil
}
