package keeper

import (
	"context"

	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/allocation/types"
)

// InitGenesis initializes the module's state from a provided genesis state: both
// streams start empty, each seeded with its own option #1.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
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
	if _, err := k.appendOption(ctx, types.STREAM_ID_HUMAN, types.AllocationOption{
		Description: "Registration rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerRegistrationRewards,
	}); err != nil {
		return err
	}
	if _, err := k.appendOption(ctx, types.STREAM_ID_CAPITAL, types.AllocationOption{
		Description: "Volume-weighted LP rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerLPRewards,
	}); err != nil {
		return err
	}

	return nil
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	genesis := types.DefaultGenesis()
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	genesis.Params = params
	return genesis, nil
}
