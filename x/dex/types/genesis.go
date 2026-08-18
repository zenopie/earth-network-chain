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

	return gs.Params.Validate()
}
