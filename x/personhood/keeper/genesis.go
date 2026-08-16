package keeper

import (
	"context"

	"github.com/earth-network/earth/x/personhood/types"
)

// InitGenesis initializes the module's state from a provided genesis state. The
// human allocation stream and its registration-rewards option are seeded by
// x/allocation, which owns them.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	if err := k.LastBuyback.Set(ctx, 0); err != nil {
		return err
	}
	return k.RegCount.Set(ctx, 0)
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
