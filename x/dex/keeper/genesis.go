package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	var maxID uint64
	// The LP reward index advances against the sum of stored pool volumes, so an
	// import has to rebuild that denominator rather than leave it at zero.
	totalVolume := math.ZeroInt()
	for _, elem := range genState.PoolMap {
		if elem.VolumeWeight.IsNil() {
			elem.VolumeWeight = math.ZeroInt()
		}
		if err := k.SetPool(ctx, elem.PoolId, elem); err != nil {
			return err
		}
		if err := k.PoolByToken.Set(ctx, elem.ReserveToken.Denom, elem.PoolId); err != nil {
			return err
		}
		if err := k.PoolLpIndex.Set(ctx, elem.PoolId, math.ZeroInt()); err != nil {
			return err
		}
		totalVolume = totalVolume.Add(elem.VolumeWeight)
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
	// The index restarts at zero above, so nothing is owed against it yet; what
	// is carried here is the dust ExportGenesis left after settling every pool.
	pending := genState.PendingLpRewards
	if pending.IsNil() {
		pending = math.ZeroInt()
	}
	if err := k.PendingLpRewards.Set(ctx, pending); err != nil {
		return err
	}
	if !genState.VolumeIndex.IsNil() && genState.VolumeIndex.IsPositive() {
		if err := k.VolumeIndex.Set(ctx, genState.VolumeIndex); err != nil {
			return err
		}
	}
	if genState.VolumeIndexDay > 0 {
		if err := k.VolumeIndexDay.Set(ctx, genState.VolumeIndexDay); err != nil {
			return err
		}
	}
	// Every pool with volume has to be on the staleness clock or it is never
	// swept. A genesis that predates carrying the queue gets a fresh timer from
	// the genesis block, which is the most a pool could have had left anyway.
	due := make(map[uint64]int64, len(genState.PoolStaleDue))
	for _, d := range genState.PoolStaleDue {
		due[d.PoolId] = d.Due
	}
	genesisTime := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	for _, elem := range genState.PoolMap {
		if elem.VolumeWeight.IsNil() || !elem.VolumeWeight.IsPositive() {
			continue
		}
		at, ok := due[elem.PoolId]
		if !ok {
			at = genesisTime + types.PoolStaleSeconds
		}
		if err := k.PoolStaleDue.Set(ctx, elem.PoolId, at); err != nil {
			return err
		}
		if err := k.PoolStaleQueue.Set(ctx, collections.Join(at, elem.PoolId)); err != nil {
			return err
		}
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
		if err := k.setLpUnbonding(ctx, key, u); err != nil {
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
	// Each pool is written as if settled. InitGenesis restarts the LP reward
	// index at zero, so a share accrued against the old index and not yet in
	// a reserve would otherwise be lost to the pool and left on the module
	// account as ERTH nobody is owed.
	lpIdx, err := k.getLpRewardIndex(ctx)
	if err != nil {
		return nil, err
	}
	settled := math.ZeroInt()
	if err := k.Pool.Walk(ctx, nil, func(id uint64, val types.Pool) (stop bool, err error) {
		last, err := k.getPoolLpIndex(ctx, id)
		if err != nil {
			return true, err
		}
		if vol := val.VolumeWeight; !vol.IsNil() && vol.IsPositive() {
			owed := vol.Mul(lpIdx.Sub(last)).Quo(lpIndexPrecision)
			if owed.IsPositive() {
				val.ReserveErth = val.ReserveErth.AddAmount(owed)
				settled = settled.Add(owed)
			}
		}
		genesis.PoolMap = append(genesis.PoolMap, val)
		return false, nil
	}); err != nil {
		return nil, err
	}
	pending, err := k.getPendingLpRewards(ctx)
	if err != nil {
		return nil, err
	}
	genesis.PendingLpRewards = pending.Sub(settled)
	if genesis.PendingLpRewards.IsNegative() {
		return nil, types.ErrInvariantBroken.Wrapf(
			"pools are owed %s of LP rewards but only %s is pending", settled, pending)
	}
	if v, err := k.VolumeIndex.Get(ctx); err == nil {
		genesis.VolumeIndex = v
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if d, err := k.VolumeIndexDay.Get(ctx); err == nil {
		genesis.VolumeIndexDay = d
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.PoolStaleDue.Walk(ctx, nil, func(id uint64, at int64) (stop bool, err error) {
		genesis.PoolStaleDue = append(genesis.PoolStaleDue, types.PoolStaleDue{PoolId: id, Due: at})
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
