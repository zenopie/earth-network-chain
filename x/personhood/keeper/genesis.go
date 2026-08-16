package keeper

import (
	"context"

	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/personhood/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	// Initialize the democratic allocation stream and seed option #1
	// (registration rewards).
	if err := k.DemRewardIndex.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}
	if err := k.DemTotalWeight.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}
	if err := k.DemLastUpkeep.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.DemEpoch.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.LastBuyback.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.RegCount.Set(ctx, 0); err != nil {
		return err
	}
	if err := k.DemSeq.Set(ctx, types.RegistrationRewardOptionID); err != nil {
		return err
	}
	if _, err := k.appendOption(ctx, types.DemocraticOption{
		Description: "Registration rewards",
		Kind:        types.DEMOCRATIC_KIND_INTEGRATED,
		Handler:     types.HandlerRegistrationRewards,
	}); err != nil {
		return err
	}

	return nil
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error

	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	return genesis, nil
}
