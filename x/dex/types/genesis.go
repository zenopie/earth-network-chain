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
		// Reserves must be real numbers before anything prices against them.
		// A nil math.Int survives proto import and panics on first use rather
		// than erroring — payoutUnbonding multiplies by ReserveErth.Amount, and
		// the swap maths divides by both — so a node importing such a genesis
		// dies with no diagnosis. CheckVolumeAccounting already guards
		// VolumeWeight.IsNil() for exactly this reason; the reserves had no
		// equivalent.
		//
		// Non-positive is refused too, not merely nil: a zero reserve makes the
		// constant-product maths degenerate and every share price undefined.
		if elem.ReserveErth.Amount.IsNil() || !elem.ReserveErth.Amount.IsPositive() {
			return fmt.Errorf("pool %d: reserve_erth must be positive, got %s",
				elem.PoolId, elem.ReserveErth.Amount)
		}
		if elem.ReserveToken.Amount.IsNil() || !elem.ReserveToken.Amount.IsPositive() {
			return fmt.Errorf("pool %d: reserve_token must be positive, got %s",
				elem.PoolId, elem.ReserveToken.Amount)
		}
		if elem.VolumeWeight.IsNil() {
			continue // read as zero on import; see keeper/genesis.go
		}
		if elem.VolumeWeight.IsNegative() {
			return fmt.Errorf("pool %d: volume_weight must not be negative, got %s",
				elem.PoolId, elem.VolumeWeight)
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

	// The same treatment PolBurns gets, and for a sharper reason: this list had
	// no validation at all, and keeper/genesis.go checked only that the address
	// decoded.
	//
	// Every field here is one the sweep would otherwise trip over at maturity,
	// in the EndBlocker, at a height nobody chose:
	//
	//   - a PoolId with no pool     -> Pool.Get errors
	//   - nil or non-positive shares -> a nil big.Int deref, which PANICS
	//   - a denom that is not this pool's -> burns one pool's supply while
	//     paying out of another's reserves, and trips the invariant pointing at
	//     the wrong module
	//
	// SweepMaturedUnbondings no longer halts on any of them — it drops the entry
	// and says so — but a dropped entry is a provider's liquidity not being
	// returned. Refusing the file is the outcome that loses nobody anything, and
	// it is available here because a genesis is inspected before it is run.
	unbondSeen := make(map[string]struct{}, len(gs.LpUnbondings))
	for _, u := range gs.LpUnbondings {
		if _, ok := poolIndexMap[fmt.Sprint(u.PoolId)]; !ok {
			return fmt.Errorf("lp unbonding for %s: no pool %d", u.Address, u.PoolId)
		}
		// Completion time and pool and address form the store key, so a repeat
		// would silently overwrite rather than restore both.
		k := fmt.Sprintf("%d/%d/%s", u.CompletionTime, u.PoolId, u.Address)
		if _, ok := unbondSeen[k]; ok {
			return fmt.Errorf("duplicated lp unbonding for %s in pool %d at %d",
				u.Address, u.PoolId, u.CompletionTime)
		}
		unbondSeen[k] = struct{}{}
		if u.Shares.Amount.IsNil() || !u.Shares.Amount.IsPositive() {
			return fmt.Errorf("lp unbonding for %s in pool %d: shares must be positive, got %s",
				u.Address, u.PoolId, u.Shares.Amount)
		}
		if want := LPShareDenom(u.PoolId); u.Shares.Denom != want {
			return fmt.Errorf("lp unbonding for %s in pool %d: shares are denominated in %s, not %s",
				u.Address, u.PoolId, u.Shares.Denom, want)
		}
		if u.CompletionTime < 0 {
			return fmt.Errorf("lp unbonding for %s in pool %d: completion_time must not be negative",
				u.Address, u.PoolId)
		}
	}

	return gs.Params.Validate()
}
