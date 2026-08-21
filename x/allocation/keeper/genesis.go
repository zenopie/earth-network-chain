package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/allocation/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
//
// An empty stream list is a fresh chain: both streams start at zero and each is
// seeded with its own option #1. A non-empty one is an import and is restored
// exactly as exported, seeding nothing — re-seeding on top of an import would
// hand out option id 1 a second time and silently repoint every voter who had
// allocated to the first one.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	if len(genState.Streams) > 0 {
		for _, st := range genState.Streams {
			if err := k.restoreStream(ctx, st); err != nil {
				return err
			}
		}
		return nil
	}

	for _, stream := range types.Streams {
		if err := k.RewardIndex.Set(ctx, key(stream), math.ZeroInt()); err != nil {
			return err
		}
		if err := k.TotalWeight.Set(ctx, key(stream), math.ZeroInt()); err != nil {
			return err
		}
		if err := k.Epoch.Set(ctx, key(stream), 0); err != nil {
			return err
		}
		if err := k.LastUpkeep.Set(ctx, key(stream), 0); err != nil {
			return err
		}
		if err := k.OptionSeq.Set(ctx, key(stream), 0); err != nil {
			return err
		}
	}

	// Option #1 of each stream. Both are INTEGRATED and both are id 1 — ids are
	// per stream, so they do not collide, and each is resolved only by the
	// handler registered for its own stream.
	if _, err := k.appendOption(ctx, types.STREAM_ID_CARETAKER, types.AllocationOption{
		Description: "Registration rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerRegistrationRewards,
	}); err != nil {
		return err
	}
	if _, err := k.appendOption(ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
		Description: "Volume-weighted LP rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerLPRewards,
	}); err != nil {
		return err
	}

	return nil
}

// restoreStream writes one exported stream back verbatim, rebuilding the only
// index that is derived: the set of integrated options, which is just the
// options whose kind says so.
func (k Keeper) restoreStream(ctx context.Context, st types.StreamState) error {
	kk := key(st.Stream)

	if err := k.RewardIndex.Set(ctx, kk, st.RewardIndex); err != nil {
		return err
	}
	if err := k.TotalWeight.Set(ctx, kk, st.TotalWeight); err != nil {
		return err
	}
	if err := k.Epoch.Set(ctx, kk, st.Epoch); err != nil {
		return err
	}
	if err := k.LastUpkeep.Set(ctx, kk, st.LastUpkeep); err != nil {
		return err
	}
	if err := k.OptionSeq.Set(ctx, kk, st.OptionSeq); err != nil {
		return err
	}

	for _, opt := range st.Options {
		if err := k.Options.Set(ctx, optionKey(st.Stream, opt.Id), opt); err != nil {
			return err
		}
		if opt.Kind == types.ALLOCATION_KIND_INTEGRATED {
			if err := k.IntegratedOptions.Set(ctx, optionKey(st.Stream, opt.Id)); err != nil {
				return err
			}
		}
	}

	for _, v := range st.Voters {
		addrBz, err := k.addressCodec.StringToBytes(v.Address)
		if err != nil {
			return err
		}
		if err := k.Voters.Set(ctx, voterKey(st.Stream, addrBz), v.Voter); err != nil {
			return err
		}
	}

	return nil
}

// ExportGenesis returns the module's exported genesis.
//
// Everything a stream needs to resume where it stopped: the reward index and the
// weight it divides by, the epoch that decides whether a voter's record still
// counts, the id sequence, and every option and vote. Exporting only the params
// — which is what this did — silently discarded every option anyone had paid to
// add, every allocation anyone had set, and every option's accrued but unclaimed
// rewards.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	genesis := types.DefaultGenesis()
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	genesis.Params = params

	for _, stream := range types.Streams {
		st, err := k.exportStream(ctx, stream)
		if err != nil {
			return nil, err
		}
		genesis.Streams = append(genesis.Streams, st)
	}

	return genesis, nil
}

func (k Keeper) exportStream(ctx context.Context, stream types.StreamId) (types.StreamState, error) {
	idx, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	total, err := k.getTotalWeight(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	seq, err := k.OptionSeq.Get(ctx, key(stream))
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return types.StreamState{}, err
	}

	st := types.StreamState{
		Stream:      stream,
		RewardIndex: idx,
		TotalWeight: total,
		Epoch:       epoch,
		// Deliberately zero: see last_upkeep in genesis.proto. Carrying the
		// cursor would advance the index by the whole restart gap in one block.
		LastUpkeep: 0,
		OptionSeq:  seq,
	}

	rng := collections.NewPrefixedPairRange[uint32, uint64](key(stream))
	if err := k.Options.Walk(ctx, rng,
		func(_ collections.Pair[uint32, uint64], opt types.AllocationOption) (bool, error) {
			st.Options = append(st.Options, opt)
			return false, nil
		}); err != nil {
		return types.StreamState{}, err
	}

	vrng := collections.NewPrefixedPairRange[uint32, []byte](key(stream))
	if err := k.Voters.Walk(ctx, vrng,
		func(pk collections.Pair[uint32, []byte], v types.Voter) (bool, error) {
			addr, err := k.addressCodec.BytesToString(pk.K2())
			if err != nil {
				return true, err
			}
			st.Voters = append(st.Voters, types.VoterEntry{Address: addr, Voter: v})
			return false, nil
		}); err != nil {
		return types.StreamState{}, err
	}

	return st, nil
}
