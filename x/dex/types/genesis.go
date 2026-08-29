package types

import (
	"fmt"
)

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params:  DefaultParams(),
		PoolMap: []Pool{}}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	poolIndexMap := make(map[string]struct{})

	for _, elem := range gs.PoolMap {
		index := fmt.Sprint(elem.PoolId)
		if _, ok := poolIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for pool")
		}
		poolIndexMap[index] = struct{}{}
		// See SetPool: an LP share denom on the spoke side makes the module's
		// own share holdings read as a surplus and halts the chain from the
		// EndBlocker. Refusing it here means a bad genesis fails to start
		// rather than starting and stopping on the first block.
		if IsLPShareDenom(elem.ReserveToken.Denom) {
			return fmt.Errorf("pool %d: %s is an lp share denom and cannot be a pool asset",
				elem.PoolId, elem.ReserveToken.Denom)
		}
		if IsLPShareDenom(elem.ReserveErth.Denom) {
			return fmt.Errorf("pool %d: %s is an lp share denom and cannot be a pool asset",
				elem.PoolId, elem.ReserveErth.Denom)
		}
	}

	if a := gs.LiquidityAuction; a != nil {
		if a.ErthForBidders.IsNil() || !a.ErthForBidders.IsValid() || a.ErthForBidders.IsZero() {
			return fmt.Errorf("liquidity auction: erth_for_bidders must be a positive coin")
		}
		if a.ErthForPool.IsNil() || !a.ErthForPool.IsValid() || a.ErthForPool.IsZero() {
			return fmt.Errorf("liquidity auction: erth_for_pool must be a positive coin")
		}
		if a.ErthForBidders.Denom != a.ErthForPool.Denom {
			return fmt.Errorf("liquidity auction: both earmarks must be the hub denom, got %s and %s",
				a.ErthForBidders.Denom, a.ErthForPool.Denom)
		}
		// The two halves must be equal. That equality is the whole reason the
		// pool opens at the price the auction cleared at: bidders pay
		// total_raised for erth_for_bidders, and the pool is seeded with the
		// same ERTH against the same total_raised. Allow them to drift and the
		// pool opens somewhere other than the clearing price, with the gap free
		// to whoever trades first.
		if !a.ErthForBidders.Amount.Equal(a.ErthForPool.Amount) {
			return fmt.Errorf("liquidity auction: earmarks must be equal, got %s and %s",
				a.ErthForBidders, a.ErthForPool)
		}
		if a.Status == AUCTION_STATUS_OPEN && a.BidDenom == "" {
			return fmt.Errorf("liquidity auction: an open auction needs a bid denom")
		}
	}

	seen := make(map[string]struct{}, len(gs.AuctionBids))
	for _, b := range gs.AuctionBids {
		if _, ok := seen[b.Bidder]; ok {
			return fmt.Errorf("duplicated auction bid for %s", b.Bidder)
		}
		seen[b.Bidder] = struct{}{}
		if b.Amount.IsNil() || !b.Amount.IsPositive() {
			return fmt.Errorf("auction bid for %s must be positive", b.Bidder)
		}
	}

	// A retirement schedule with no pool, or one written twice, would either
	// panic the EndBlocker or retire the position at double speed.
	polSeen := make(map[uint64]struct{}, len(gs.PolBurns))
	for _, b := range gs.PolBurns {
		if _, ok := poolIndexMap[fmt.Sprint(b.PoolId)]; !ok {
			return fmt.Errorf("pol burn: no pool %d", b.PoolId)
		}
		if _, ok := polSeen[b.PoolId]; ok {
			return fmt.Errorf("duplicated pol burn for pool %d", b.PoolId)
		}
		polSeen[b.PoolId] = struct{}{}
		if b.TotalShares.IsNil() || !b.TotalShares.IsPositive() {
			return fmt.Errorf("pol burn for pool %d: total_shares must be positive", b.PoolId)
		}
		if !b.SharesRemaining.IsNil() {
			if b.SharesRemaining.IsNegative() || b.SharesRemaining.GT(b.TotalShares) {
				return fmt.Errorf("pol burn for pool %d: shares_remaining %s is not within total_shares %s",
					b.PoolId, b.SharesRemaining, b.TotalShares)
			}
		}
		if b.DurationSeconds <= 0 {
			return fmt.Errorf("pol burn for pool %d: duration_seconds must be positive", b.PoolId)
		}
		if b.StartTime < 0 {
			return fmt.Errorf("pol burn for pool %d: start_time must not be negative", b.PoolId)
		}
	}

	return gs.Params.Validate()
}
