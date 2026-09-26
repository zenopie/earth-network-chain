package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/earth/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}
	for _, b := range genState.Burned {
		if err := k.setBurned(ctx, b.Source, b.Amount); err != nil {
			return err
		}
	}
	// Zero means "never minted", which is what a new chain wants and what
	// MintEmission already treats as a signal to start the clock rather than
	// mint against the unix epoch. Writing it unconditionally is therefore safe
	// and keeps the import symmetric with the export.
	return k.LastMintTime.Set(ctx, resumeClock(ctx, genState.LastMintTime))
}

// resumeClock is an imported emission timestamp, moved up to the genesis time.
//
// An export carries the time of the last block it saw, and the chain that
// imports it starts at its own genesis time, however much later that is. The
// first block used to mint the whole gap in one go — a relaunch a month after
// the export paid a month of staking emission to whoever was validating in
// block one. Time between export and genesis is not time the chain ran.
// Zero, "never minted", stays zero.
func resumeClock(ctx context.Context, last int64) int64 {
	if genesis := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano(); last != 0 && genesis > last {
		return genesis
	}
	return last
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error

	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	genesis.Burned, err = k.BurnedBySource(ctx)
	if err != nil {
		return nil, err
	}

	genesis.LastMintTime, err = k.GetLastMintTime(ctx)
	if err != nil {
		return nil, err
	}

	return genesis, nil
}
