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
	tallies := map[uint64]types.Tally{}
	for _, v := range gs.ProposalVotes {
		if err := k.ProposalVotes.Set(ctx, collKey(v.ProposalId, v.Nullifier), int32(v.Option)); err != nil {
			return err
		}
		tallies[v.ProposalId] = add(tallies[v.ProposalId], v.Option)
	}
	for id, tally := range tallies {
		if err := k.ProposalTally.Set(ctx, id, tally); err != nil {
			return err
		}
	}

	for _, entry := range gs.RemovalBallots {
		ballot := entry.Ballot
		ballot.Tally = types.Tally{}
		for _, v := range entry.Votes {
			if err := k.RemovalVotes.Set(ctx, collKey(ballot.OptionId, v.Nullifier), int32(v.Option)); err != nil {
				return err
			}
			ballot.Tally = add(ballot.Tally, v.Option)
		}
		if err := k.RemovalBallots.Set(ctx, ballot.OptionId, ballot); err != nil {
			return err
		}
		if err := k.RemovalQueue.Set(ctx, collections.Join(ballot.ClosesAt, ballot.OptionId)); err != nil {
			return err
		}
	}
	return nil
}

// ExportGenesis writes out the votes still in flight.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	gs := types.DefaultGenesis()

	if err := k.ProposalVotes.Walk(ctx, nil, func(key collections.Pair[uint64, []byte], option int32) (bool, error) {
		gs.ProposalVotes = append(gs.ProposalVotes, types.ProposalVoteEntry{
			ProposalId: key.K1(),
			Nullifier:  key.K2(),
			Option:     types.VoteOption(option),
		})
		return false, nil
	}); err != nil {
		return nil, err
	}

	if err := k.RemovalBallots.Walk(ctx, nil, func(optionID uint64, ballot types.RemovalBallot) (bool, error) {
		entry := types.RemovalBallotEntry{Ballot: ballot}
		rng := collections.NewPrefixedPairRange[uint64, []byte](optionID)
		if err := k.RemovalVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], option int32) (bool, error) {
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

func add(t types.Tally, o types.VoteOption) types.Tally {
	switch o {
	case types.VOTE_OPTION_YES:
		t.Yes++
	case types.VOTE_OPTION_NO:
		t.No++
	}
	return t
}
