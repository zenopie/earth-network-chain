package keeper

import (
	"context"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/assembly/types"
)

// InitGenesis restores votes that were in flight.
//
// The tallies are rebuilt from the votes rather than imported alongside them. A
// count carried separately from the things it counts is a number that can drift
// from them, and an import is exactly where such a drift would enter without
// anyone noticing — the ballot would then close on a figure no set of votes
// supports.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	// Each proposal's votes go on a fresh ballot, and recordVote rebuilds its
	// tally from them one vote at a time.
	for _, v := range gs.ProposalVotes {
		ballot, ok, err := k.proposalBallot(ctx, v.ProposalId)
		if err != nil {
			return err
		}
		if !ok {
			if ballot, err = k.newBallot(ctx); err != nil {
				return err
			}
			if err := k.ProposalBallot.Set(ctx, v.ProposalId, ballot); err != nil {
				return err
			}
		}
		if _, err := k.recordVote(ctx, ballot, v.Nullifier, v.Option); err != nil {
			return err
		}
	}

	for _, entry := range gs.RemovalBallots {
		record := entry.Ballot
		// The tally lives with the ballot, not in this record; see removalTally.
		record.Tally = types.Tally{}
		if err := k.RemovalBallots.Set(ctx, record.OptionId, record); err != nil {
			return err
		}
		if err := k.RemovalQueue.Set(ctx, collections.Join(record.ClosesAt, record.OptionId)); err != nil {
			return err
		}
		ballot, err := k.newBallot(ctx)
		if err != nil {
			return err
		}
		if err := k.RemovalBallotID.Set(ctx, record.OptionId, ballot); err != nil {
			return err
		}
		for _, v := range entry.Votes {
			if _, err := k.recordVote(ctx, ballot, v.Nullifier, v.Option); err != nil {
				return err
			}
		}
	}
	return nil
}

// ExportGenesis writes out the votes still in flight. Closed ballots waiting to
// be cleared are left out: nothing counts them any more.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	gs := types.DefaultGenesis()

	if err := k.ProposalBallot.Walk(ctx, nil, func(proposalID, ballot uint64) (bool, error) {
		rng := collections.NewPrefixedPairRange[uint64, []byte](ballot)
		return false, k.BallotVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], option int32) (bool, error) {
			gs.ProposalVotes = append(gs.ProposalVotes, types.ProposalVoteEntry{
				ProposalId: proposalID,
				Nullifier:  key.K2(),
				Option:     types.VoteOption(option),
			})
			return false, nil
		})
	}); err != nil {
		return nil, err
	}

	if err := k.RemovalBallots.Walk(ctx, nil, func(optionID uint64, record types.RemovalBallot) (bool, error) {
		ballot, err := k.RemovalBallotID.Get(ctx, optionID)
		if err != nil {
			return true, err
		}
		if record.Tally, err = k.ballotTally(ctx, ballot); err != nil {
			return true, err
		}
		entry := types.RemovalBallotEntry{Ballot: record}
		rng := collections.NewPrefixedPairRange[uint64, []byte](ballot)
		if err := k.BallotVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], option int32) (bool, error) {
			entry.Votes = append(entry.Votes, types.RemovalVoteEntry{
				Nullifier: key.K2(),
				Option:    types.VoteOption(option),
			})
			return false, nil
		}); err != nil {
			return true, err
		}
		gs.RemovalBallots = append(gs.RemovalBallots, entry)
		return false, nil
	}); err != nil {
		return nil, err
	}

	return gs, nil
}
