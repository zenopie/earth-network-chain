package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/assembly/types"
)

// collKey builds the (ballot, nullifier) key both vote maps use.
func collKey(id uint64, nullifier []byte) collections.Pair[uint64, []byte] {
	return collections.Join(id, nullifier)
}

// proposalTally reads a proposal's human tally, defaulting to an empty one.
//
// An empty tally is not an absent answer: it is no votes cast, which does not
// carry. Silence is a refusal in this chamber, deliberately — see
// types.Approves.
func (k Keeper) proposalTally(ctx context.Context, id uint64) (types.Tally, error) {
	tally, err := k.ProposalTally.Get(ctx, id)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.Tally{}, nil
		}
		return types.Tally{}, err
	}
	return tally, nil
}

// purgeProposalVotes clears a settled proposal's votes and tally.
//
// Collected before removing because the walk and the removal are over the same
// map, and a collections walk does not promise to survive writes underneath it.
func (k Keeper) purgeProposalVotes(ctx context.Context, id uint64) error {
	var keys []collections.Pair[uint64, []byte]
	rng := collections.NewPrefixedPairRange[uint64, []byte](id)
	if err := k.ProposalVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], _ int32) (bool, error) {
		keys = append(keys, key)
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := k.ProposalVotes.Remove(ctx, key); err != nil {
			return err
		}
	}
	return k.ProposalTally.Remove(ctx, id)
}

// closeRemovalBallot clears a finished ballot, its queue entry and its votes.
func (k Keeper) closeRemovalBallot(ctx context.Context, queueKey collections.Pair[int64, uint64], optionID uint64) error {
	var keys []collections.Pair[uint64, []byte]
	rng := collections.NewPrefixedPairRange[uint64, []byte](optionID)
	if err := k.RemovalVotes.Walk(ctx, rng, func(key collections.Pair[uint64, []byte], _ int32) (bool, error) {
		keys = append(keys, key)
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := k.RemovalVotes.Remove(ctx, key); err != nil {
			return err
		}
	}
	if err := k.RemovalBallots.Remove(ctx, optionID); err != nil {
		return err
	}
	return k.RemovalQueue.Remove(ctx, queueKey)
}
