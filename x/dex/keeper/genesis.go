package keeper

import (
	"context"
	"errors"

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

	// The auction's earmarks are pre-funded: the ERTH is already in the module
	// account's genesis balance, and this only records how much of it is spoken
	// for. Nil leaves the auction unconfigured, and every auction message then
	// fails with ErrAuctionUnavailable.
	if genState.LiquidityAuction != nil {
		a := *genState.LiquidityAuction
		if a.TotalRaised.IsNil() {
			a.TotalRaised = math.ZeroInt()
		}
		if a.Claimed.IsNil() {
			a.Claimed = math.ZeroInt()
		}
		if err := k.LiquidityAuction.Set(ctx, a); err != nil {
			return err
		}
	}
	for _, b := range genState.AuctionBids {
		addrBz, err := k.addressCodec.StringToBytes(b.Bidder)
		if err != nil {
			return err
		}
		if b.Amount.IsNil() {
			b.Amount = math.ZeroInt()
		}
		if err := k.AuctionBids.Set(ctx, addrBz, b); err != nil {
			return err
		}
	}

	// Protocol-owned liquidity retirement. A schedule with start_time 0 anchors
	// itself on the first block that walks it, which is how the genesis file
	// writes the ANML/ERTH schedule: it cannot know the chain's first block time.
	for _, b := range genState.PolBurns {
		if b.SharesRemaining.IsNil() {
			b.SharesRemaining = b.TotalShares
		}
		if err := k.PolBurns.Set(ctx, b.PoolId, b); err != nil {
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

	if a, err := k.LiquidityAuction.Get(ctx); err == nil {
		genesis.LiquidityAuction = &a
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.AuctionBids.Walk(ctx, nil, func(_ []byte, val types.AuctionBid) (stop bool, err error) {
		genesis.AuctionBids = append(genesis.AuctionBids, val)
		return false, nil
	}); err != nil {
		return nil, err
	}

	// Exported with their resolved start times, so an upgrade resumes a schedule
	// part-way through rather than restarting its ten years.
	if err := k.PolBurns.Walk(ctx, nil, func(_ uint64, val types.PolBurn) (stop bool, err error) {
		genesis.PolBurns = append(genesis.PolBurns, val)
		return false, nil
	}); err != nil {
		return nil, err
	}

	return genesis, nil
}
