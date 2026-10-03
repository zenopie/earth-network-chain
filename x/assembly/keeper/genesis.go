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
	// Removal ballots keep their ids (their scope is derived from it), so the
	// sequence resumes past every id already handed out.
	if err := k.BallotSeq.Set(ctx, gs.BallotSeq); err != nil {
		return err
	}
	// Rounds and votes of a proposal x/gov (initialised first) does not have
	// in its voting period are dead: nothing would ever close their ballot
	// (audit 5 L-AS4). They are not imported.
	voting := func(id uint64) (bool, error) {
		if k.gov == nil {
			return true, nil
		}
		return k.gov.VotingPeriodProposals.Has(ctx, id)
	}
	for _, r := range gs.ProposalRounds {
		if ok, err := voting(r.ProposalId); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := k.ProposalRound.Set(ctx, r.ProposalId, r.Round); err != nil {
			return err
		}
	}
	// Each proposal's votes go on a fresh ballot, and recordVote rebuilds its
	// tally from them one vote at a time.
	for _, v := range gs.ProposalVotes {
		if ok, err := voting(v.ProposalId); err != nil {
			return err
		} else if !ok {
			continue
		}
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

	for _, c := range gs.RemovalCooldowns {
		if err := k.RemovalCooldown.Set(ctx, c.OptionId, c.Until); err != nil {
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
		ballot := record.BallotId
		if err := k.BallotTally.Set(ctx, ballot, types.Tally{}); err != nil {
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

	// Subjects are carried (audit 4, C6): they were fixed as each proposal
	// entered voting, and recomputing them against the relaunch's trust store
	// could change who may vote mid-vote. Only a proposal in voting without
	// an entry is classified here, from x/gov's and x/pki's state, both
	// imported before this module (classifyProposal keeps a stored entry).
	for _, e := range gs.ProposalSubjects {
		if err := k.Subjects.Set(ctx, e.ProposalId, e.Subjects); err != nil {
			return err
		}
	}
	return k.gov.VotingPeriodProposals.Walk(ctx, nil, func(id uint64, _ []byte) (bool, error) {
		p, err := k.gov.Proposals.Get(ctx, id)
		if err != nil {
			return true, err
		}
		return false, k.classifyProposal(ctx, p)
	})
}

// ExportGenesis writes out the votes still in flight. Closed ballots waiting to
// be cleared are left out: nothing counts them any more.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	gs := types.DefaultGenesis()
	seq, err := k.BallotSeq.Peek(ctx)
	if err != nil {
		return nil, err
	}
	gs.BallotSeq = seq
	if err := k.ProposalRound.Walk(ctx, nil, func(id uint64, r types.ProposalRound) (bool, error) {
		gs.ProposalRounds = append(gs.ProposalRounds, types.ProposalRoundEntry{ProposalId: id, Round: r})
		return false, nil
	}); err != nil {
		return nil, err
	}

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

	if err := k.Subjects.Walk(ctx, nil, func(id uint64, subj types.ProposalSubjects) (bool, error) {
		gs.ProposalSubjects = append(gs.ProposalSubjects, types.ProposalSubjectsEntry{ProposalId: id, Subjects: subj})
		return false, nil
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

	if err := k.RemovalCooldown.Walk(ctx, nil, func(optionID uint64, until int64) (bool, error) {
		gs.RemovalCooldowns = append(gs.RemovalCooldowns, types.RemovalCooldownEntry{OptionId: optionID, Until: until})
		return false, nil
	}); err != nil {
		return nil, err
	}

	return gs, nil
}
