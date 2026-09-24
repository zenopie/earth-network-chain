package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/assembly/types"
)

// collKey builds the (ballot, nullifier) key the vote map uses.
func collKey(id uint64, nullifier []byte) collections.Pair[uint64, []byte] {
	return collections.Join(id, nullifier)
}

// ballotTally reads an open ballot's tally, defaulting to an empty one.
//
// An empty tally is not an absent answer: it is no votes cast, which does not
// carry. Silence is a refusal in this chamber, deliberately — see
// types.Approves.
func (k Keeper) ballotTally(ctx context.Context, ballot uint64) (types.Tally, error) {
	tally, err := k.BallotTally.Get(ctx, ballot)
	if errors.Is(err, collections.ErrNotFound) {
		return types.Tally{}, nil
	}
	return tally, err
}

// newBallot opens a ballot and returns its id.
//
// The tally entry is written empty rather than left absent, because its
// presence is what marks a ballot open — OnRegistrationRetired uses it to tell
// a vote that still counts from one waiting to be cleared.
func (k Keeper) newBallot(ctx context.Context) (uint64, error) {
	id, err := k.BallotSeq.Next(ctx)
	if err != nil {
		return 0, err
	}
	// Ballot ids start at 1, so 0 can never be mistaken for one.
	id++
	return id, k.BallotTally.Set(ctx, id, types.Tally{})
}

// closeBallot ends a ballot in O(1): its tally goes, which is what stops
// anything counting it, and its votes are queued for purgeClosedBallots.
func (k Keeper) closeBallot(ctx context.Context, ballot uint64) error {
	if err := k.BallotTally.Remove(ctx, ballot); err != nil {
		return err
	}
	return k.ClosedBallots.Set(ctx, ballot)
}

// proposalBallot returns the open ballot on a proposal, if it has one.
func (k Keeper) proposalBallot(ctx context.Context, proposalID uint64) (uint64, bool, error) {
	ballot, err := k.ProposalBallot.Get(ctx, proposalID)
	if errors.Is(err, collections.ErrNotFound) {
		return 0, false, nil
	}
	return ballot, err == nil, err
}

// proposalTally is the human tally on a proposal's current round.
func (k Keeper) proposalTally(ctx context.Context, proposalID uint64) (types.Tally, error) {
	ballot, ok, err := k.proposalBallot(ctx, proposalID)
	if err != nil || !ok {
		return types.Tally{}, err
	}
	return k.ballotTally(ctx, ballot)
}

// endProposalRound closes the proposal's current ballot, if any. A later vote
// on the same proposal — after an expedited demotion — opens a fresh one.
func (k Keeper) endProposalRound(ctx context.Context, proposalID uint64) error {
	ballot, ok, err := k.proposalBallot(ctx, proposalID)
	if err != nil || !ok {
		return err
	}
	if err := k.ProposalBallot.Remove(ctx, proposalID); err != nil {
		return err
	}
	return k.closeBallot(ctx, ballot)
}

// removalTally is the tally of the open removal ballot on an option.
func (k Keeper) removalTally(ctx context.Context, optionID uint64) (types.Tally, error) {
	ballot, err := k.RemovalBallotID.Get(ctx, optionID)
	if errors.Is(err, collections.ErrNotFound) {
		return types.Tally{}, nil
	} else if err != nil {
		return types.Tally{}, err
	}
	return k.ballotTally(ctx, ballot)
}

// closeRemovalBallot clears a finished removal ballot's record and queue entry,
// and closes the ballot behind it. Its votes are cleared later, in batches.
func (k Keeper) closeRemovalBallot(ctx context.Context, queueKey collections.Pair[int64, uint64], optionID uint64) error {
	ballot, err := k.RemovalBallotID.Get(ctx, optionID)
	if err != nil {
		return err
	}
	if err := k.RemovalBallotID.Remove(ctx, optionID); err != nil {
		return err
	}
	if err := k.RemovalBallots.Remove(ctx, optionID); err != nil {
		return err
	}
	if err := k.RemovalQueue.Remove(ctx, queueKey); err != nil {
		return err
	}
	return k.closeBallot(ctx, ballot)
}

// recordVote casts a vote on an open ballot, keeps its tally, and indexes the
// vote under the voter's nullifier.
func (k Keeper) recordVote(ctx context.Context, ballot uint64, nullifier []byte, option types.VoteOption) (types.Tally, error) {
	tally, err := k.ballotTally(ctx, ballot)
	if err != nil {
		return tally, err
	}
	tally, err = castVote(ctx, k.BallotVotes, collKey(ballot, nullifier), tally, option)
	if err != nil {
		return tally, err
	}
	if err := k.VotedBallots.Set(ctx, collections.Join(nullifier, ballot)); err != nil {
		return tally, err
	}
	return tally, k.BallotTally.Set(ctx, ballot, tally)
}

// OnRegistrationRetired implements x/personhood's RetirementListener: a
// registration that stops counting as a human — expired, purged under a revoked
// Document Signer — has its votes taken back, off every open ballot's tally.
//
// The running tallies are what ballots are decided on, so this is what keeps
// them true. It costs a read and a few writes per ballot that person has a vote
// on, found through VotedBallots, and x/personhood already caps how many
// registrations one block may retire — so the work lands in bounded pieces.
func (k Keeper) OnRegistrationRetired(ctx context.Context, nullifier []byte) error {
	var ballots []uint64
	rng := collections.NewPrefixedPairRange[[]byte, uint64](nullifier)
	if err := k.VotedBallots.Walk(ctx, rng, func(key collections.Pair[[]byte, uint64]) (bool, error) {
		ballots = append(ballots, key.K2())
		return false, nil
	}); err != nil {
		return err
	}

	for _, ballot := range ballots {
		key := collKey(ballot, nullifier)
		prev, err := k.BallotVotes.Get(ctx, key)
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		if err == nil {
			// Only an open ballot has a tally to take the vote off. A closed
			// one is waiting for purgeClosedBallots, and the vote is simply
			// cleared a little early.
			if tally, err := k.BallotTally.Get(ctx, ballot); err == nil {
				switch types.VoteOption(prev) {
				case types.VOTE_OPTION_YES:
					tally.Yes--
				case types.VOTE_OPTION_NO:
					tally.No--
				}
				if err := k.BallotTally.Set(ctx, ballot, tally); err != nil {
					return err
				}
			} else if !errors.Is(err, collections.ErrNotFound) {
				return err
			}
			if err := k.BallotVotes.Remove(ctx, key); err != nil {
				return err
			}
		}
		if err := k.VotedBallots.Remove(ctx, collections.Join(nullifier, ballot)); err != nil {
			return err
		}
	}
	return nil
}

// purgeClosedBallots clears the votes of closed ballots, at most limit of them
// per call, oldest ballot first.
//
// This is the work closing a ballot used to do in the block it closed, which
// made the cost of that block grow with the turnout. Nothing reads a closed
// ballot's votes, so they can wait: the backlog costs space and nothing else.
func (k Keeper) purgeClosedBallots(ctx context.Context, limit int) error {
	for limit > 0 {
		iter, err := k.ClosedBallots.Iterate(ctx, nil)
		if err != nil {
			return err
		}
		if !iter.Valid() {
			iter.Close()
			return nil
		}
		ballot, err := iter.Key()
		iter.Close()
		if err != nil {
			return err
		}

		// Collected before removing: a collections walk does not promise to
		// survive writes underneath it.
		var nullifiers [][]byte
		rng := collections.NewPrefixedPairRange[uint64, []byte](ballot)
		if err := k.BallotVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], _ int32) (bool, error) {
			nullifiers = append(nullifiers, key.K2())
			return len(nullifiers) >= limit, nil
		}); err != nil {
			return err
		}
		for _, n := range nullifiers {
			if err := k.BallotVotes.Remove(ctx, collKey(ballot, n)); err != nil {
				return err
			}
			if err := k.VotedBallots.Remove(ctx, collections.Join(n, ballot)); err != nil {
				return err
			}
		}
		limit -= len(nullifiers)
		if limit > 0 {
			// Fewer than the budget left means this ballot is clear.
			if err := k.ClosedBallots.Remove(ctx, ballot); err != nil {
				return err
			}
		}
	}
	return nil
}

// MigrateToBallots moves votes from the v0.9.0 layout, keyed by proposal and
// option id, into ballots, and empties the old maps.
//
// For the v0.9.1 upgrade. O(votes), once, at a scheduled height. earth-1 holds
// votes here only if a proposal or removal ballot is open when it runs.
func (k Keeper) MigrateToBallots(ctx context.Context) error {
	type vote struct {
		id        uint64
		nullifier []byte
		option    int32
	}
	collect := func(m collections.Map[collections.Pair[uint64, []byte], int32]) ([]vote, error) {
		var out []vote
		err := m.Walk(ctx, nil, func(key collections.Pair[uint64, []byte], option int32) (bool, error) {
			out = append(out, vote{key.K1(), key.K2(), option})
			return false, nil
		})
		return out, err
	}

	proposalVotes, err := collect(k.LegacyProposalVotes)
	if err != nil {
		return err
	}
	for _, v := range proposalVotes {
		ballot, ok, err := k.proposalBallot(ctx, v.id)
		if err != nil {
			return err
		}
		if !ok {
			if ballot, err = k.newBallot(ctx); err != nil {
				return err
			}
			if err := k.ProposalBallot.Set(ctx, v.id, ballot); err != nil {
				return err
			}
		}
		if _, err := k.recordVote(ctx, ballot, v.nullifier, types.VoteOption(v.option)); err != nil {
			return err
		}
		if err := k.LegacyProposalVotes.Remove(ctx, collKey(v.id, v.nullifier)); err != nil {
			return err
		}
	}
	if err := k.LegacyProposalTally.Clear(ctx, nil); err != nil {
		return err
	}

	// Every open removal record gets a ballot, voted on or not.
	var options []uint64
	if err := k.RemovalBallots.Walk(ctx, nil, func(optionID uint64, _ types.RemovalBallot) (bool, error) {
		options = append(options, optionID)
		return false, nil
	}); err != nil {
		return err
	}
	for _, optionID := range options {
		ballot, err := k.newBallot(ctx)
		if err != nil {
			return err
		}
		if err := k.RemovalBallotID.Set(ctx, optionID, ballot); err != nil {
			return err
		}
	}
	removalVotes, err := collect(k.LegacyRemovalVotes)
	if err != nil {
		return err
	}
	for _, v := range removalVotes {
		ballot, err := k.RemovalBallotID.Get(ctx, v.id)
		if errors.Is(err, collections.ErrNotFound) {
			// A vote on a ballot that no longer exists was dead already.
			if err := k.LegacyRemovalVotes.Remove(ctx, collKey(v.id, v.nullifier)); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if _, err := k.recordVote(ctx, ballot, v.nullifier, types.VoteOption(v.option)); err != nil {
			return err
		}
		if err := k.LegacyRemovalVotes.Remove(ctx, collKey(v.id, v.nullifier)); err != nil {
			return err
		}
	}
	return nil
}
