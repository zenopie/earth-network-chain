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
// presence is what marks a ballot open.
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

// recordVote casts a vote on an open ballot and keeps its tally. A voter's
// nullifier is the same for every proof in the ballot's scope, so a second
// vote replaces the first.
func (k Keeper) recordVote(ctx context.Context, ballot uint64, nullifier []byte, option types.VoteOption) (types.Tally, error) {
	tally, err := k.ballotTally(ctx, ballot)
	if err != nil {
		return tally, err
	}
	tally, err = castVote(ctx, k.BallotVotes, collKey(ballot, nullifier), tally, option)
	if err != nil {
		return tally, err
	}
	return tally, k.BallotTally.Set(ctx, ballot, tally)
}

// purgeClosedBallots clears the votes of closed ballots, at most limit of them
// per call, oldest ballot first.
//
// Not done in the block a ballot closes, which would make that block's cost
// grow with the turnout. Nothing reads a closed ballot's votes, so they can
// wait: the backlog costs space and nothing else.
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
