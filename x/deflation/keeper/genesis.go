package keeper

import (
	"context"

	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/deflation/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	// Initialize the stake-weighted allocation stream and seed option #1
	// (volume-weighted LP rewards).
	if err := k.RewardIndex.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}
	if err := k.TotalWeight.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}
	if err := k.AllocationEpoch.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.LastAllocUpkeep.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.AllocationSeq.Set(ctx, types.LPRewardsOptionID); err != nil {
		return err
	}
	if _, err := k.appendOption(ctx, types.AllocationOption{
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
