package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/dex/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	var maxID uint64
	// The LP reward index advances against the sum of stored pool volumes, so an
	// import has to rebuild that denominator rather than leave it at zero.
	totalVolume := math.ZeroInt()
	for _, elem := range genState.PoolMap {
		if elem.Volume.IsNil() {
			elem.Volume = math.ZeroInt()
		}
		if err := k.Pool.Set(ctx, elem.PoolId, elem); err != nil {
			return err
		}
		if err := k.PoolByToken.Set(ctx, elem.ReserveToken.Denom, elem.PoolId); err != nil {
			return err
		}
		if err := k.PoolLpIndex.Set(ctx, elem.PoolId, math.ZeroInt()); err != nil {
			return err
		}
		totalVolume = totalVolume.Add(elem.Volume)
		if elem.PoolId >= maxID {
			maxID = elem.PoolId + 1
		}
	}
	if err := k.LpRewardIndex.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}
	if err := k.LpTotalVolume.Set(ctx, totalVolume); err != nil {
		return err
	}
	// Resume the id sequence past the highest imported pool id.
	if maxID > 0 {
		if err := k.PoolSeq.Set(ctx, maxID); err != nil {
			return err
		}
	}

	// In-flight withdrawals carry escrowed shares on the module account. Dropping
	// them on import would leave those shares outstanding with nobody able to
	// redeem them, so they are restored under the same completion-time key the
	// sweep walks.
	for _, u := range genState.LpUnbondings {
		addrBz, err := k.addressCodec.StringToBytes(u.Address)
		if err != nil {
			return err
		}
		key := collections.Join3(u.CompletionTime, u.PoolId, addrBz)
		if err := k.LpUnbondings.Set(ctx, key, u); err != nil {
			return err
		}
	}

	if err := k.Params.Set(ctx, genState.Params); err != nil {
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
	if err := k.Pool.Walk(ctx, nil, func(_ uint64, val types.Pool) (stop bool, err error) {
		genesis.PoolMap = append(genesis.PoolMap, val)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.LpUnbondings.Walk(ctx, nil,
		func(_ collections.Triple[int64, uint64, []byte], val types.LpUnbonding) (stop bool, err error) {
			genesis.LpUnbondings = append(genesis.LpUnbondings, val)
			return false, nil
		}); err != nil {
		return nil, err
	}

	return genesis, nil
}
