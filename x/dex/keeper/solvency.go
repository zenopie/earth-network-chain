package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/dex/types"
)

// Solvency checking that costs the same whatever the chain's pool count.
//
// The module must hold exactly what its records say it owes, and that check has
// to run every block: the alternative is a mispriced pool quietly draining, and
// the whole pre-mine sits in this one account. Recomputing what is owed by
// walking every pool made the check O(pools), which is fine for a dozen pools
// and untenable for a chain that expects a token per project — and the walk was
// not even the whole cost, since comparing against GetAllBalances is O(denoms
// held), which is the same order again.
//
// It decomposes instead, along two structural facts:
//
//   - ERTH is commingled across every pool, so it needs a running total. One
//     number, maintained on write, compared against one bank balance.
//   - Every other denom belongs to exactly one pool, because PoolByToken allows
//     only one pool per token. So a pool's own reserve is the entire obligation
//     for its denom, and checking it needs nothing else.
//
// And on one policy fact: the dex module account is blocked from receiving
// outside transfers (blockAccAddrs, app/app_config.go). Nothing but this module
// can move its coins, so a pool nobody touched cannot have become insolvent.
// That is what makes it sound to check only the pools a block actually wrote
// rather than all of them.
//
// The bank is the witness throughout. Neither figure is trusted against itself.

// SetPool is the only writer of a pool record.
//
// Everything that changes a reserve goes through here so the ERTH running total
// moves with it, and so the pool is marked for this block's solvency check.
// Going around it would leave the total drifting from the pools it claims to
// sum, which the EndBlocker would then report as the module being short. Writing
// k.Pool.Set directly is therefore a bug; CheckErthTotalAccounting exists to
// catch one, and the exhaustive AssertInvariants runs it after every operation
// the tests perform.
func (k Keeper) SetPool(ctx context.Context, poolID uint64, pool types.Pool) error {
	prevErth := math.ZeroInt()
	if prev, err := k.Pool.Get(ctx, poolID); err == nil {
		if !prev.ReserveErth.Amount.IsNil() {
			prevErth = prev.ReserveErth.Amount
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	newErth := math.ZeroInt()
	if !pool.ReserveErth.Amount.IsNil() {
		newErth = pool.ReserveErth.Amount
	}

	if delta := newErth.Sub(prevErth); !delta.IsZero() {
		total, err := k.getTotalPoolErth(ctx)
		if err != nil {
			return err
		}
		total = total.Add(delta)
		if total.IsNegative() {
			total = math.ZeroInt()
		}
		if err := k.TotalPoolErth.Set(ctx, total); err != nil {
			return err
		}
	}

	if err := k.DirtyPools.Set(ctx, poolID); err != nil {
		return err
	}
	return k.Pool.Set(ctx, poolID, pool)
}

func (k Keeper) getTotalPoolErth(ctx context.Context) (math.Int, error) {
	v, err := k.TotalPoolErth.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

// auctionErthOwed is the ERTH the auction still has a claim on, computed from
// the auction singleton and so O(1). It mirrors the auction arm of
// AssetObligations, which remains the exhaustive version used by tests.
func (k Keeper) auctionErthOwed(ctx context.Context) (math.Int, string, error) {
	a, err := k.getAuction(ctx)
	switch {
	case errors.Is(err, types.ErrAuctionUnavailable):
		return math.ZeroInt(), "", nil
	case err != nil:
		return math.Int{}, "", err
	}

	switch a.Status {
	case types.AUCTION_STATUS_SETTLED:
		// erth_for_pool became the new pool's reserve and is already inside the
		// running pool total. What is still owed is the unclaimed part of the
		// bidders' earmark, dust included.
		if rem := a.ErthForBidders.Amount.Sub(a.Claimed); rem.IsPositive() {
			return rem, "", nil
		}
		return math.ZeroInt(), "", nil
	default:
		// PENDING or OPEN: both earmarks are still on the module account,
		// pre-funded from genesis and spoken for.
		owed := math.ZeroInt()
		if a.ErthForBidders.Amount.IsPositive() {
			owed = owed.Add(a.ErthForBidders.Amount)
		}
		if a.ErthForPool.Amount.IsPositive() {
			owed = owed.Add(a.ErthForPool.Amount)
		}
		// Bids in an open window are held until settlement pairs them with the
		// pool earmark, and no pool exists for that denom yet — pool creation is
		// locked until the auction settles, which is what keeps this obligation
		// and the per-pool one from ever describing the same coins.
		if a.Status == types.AUCTION_STATUS_OPEN && a.TotalRaised.IsPositive() && a.BidDenom != "" {
			return owed, a.BidDenom, nil
		}
		return owed, "", nil
	}
}

// checkErthSolvency compares the module's ERTH balance against what the pools
// and the auction between them are owed. O(1).
func (k Keeper) checkErthSolvency(ctx context.Context) error {
	hub, err := k.HubDenom(ctx)
	if err != nil {
		return err
	}
	pools, err := k.getTotalPoolErth(ctx)
	if err != nil {
		return err
	}
	auction, _, err := k.auctionErthOwed(ctx)
	if err != nil {
		return err
	}
	// LP rewards x/allocation has paid in that no pool has settled into its
	// reserve yet. They are on the account and they belong to the LPs, so
	// without this term they read as ERTH the module cannot account for — which
	// is exactly what they would be if the pay-in and the settle ever stopped
	// matching.
	pending, err := k.getPendingLpRewards(ctx)
	if err != nil {
		return err
	}
	owed := pools.Add(auction).Add(pending)

	held := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), hub).Amount
	switch {
	case held.LT(owed):
		return types.ErrInvariantBroken.Wrapf(
			"dex module is short %s%s: it owes %s (pools %s, auction %s, pending lp rewards %s) and holds %s",
			owed.Sub(held), hub, owed, pools, auction, pending, held)
	case held.GT(owed):
		return types.ErrInvariantBroken.Wrapf(
			"dex module holds %s%s it cannot account for: it owes %s (pools %s, auction %s, pending lp rewards %s) and holds %s",
			held.Sub(owed), hub, owed, pools, auction, pending, held)
	}
	return nil
}

// checkPoolTokenSolvency compares one pool's spoke reserve against the module's
// balance of that denom. Exact, because one pool per token means no other pool
// has a claim on it.
func (k Keeper) checkPoolTokenSolvency(ctx context.Context, poolID uint64) error {
	pool, err := k.Pool.Get(ctx, poolID)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil // retired between being marked and being checked
		}
		return err
	}
	denom := pool.ReserveToken.Denom
	if denom == "" {
		return nil
	}
	// The auction's bid denom is owed on the auction's account until settlement
	// creates its pool, so while that is true the two claims would double-count.
	if _, bidDenom, err := k.auctionErthOwed(ctx); err != nil {
		return err
	} else if bidDenom == denom {
		return nil
	}

	held := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), denom).Amount
	owed := pool.ReserveToken.Amount
	if owed.IsNil() {
		owed = math.ZeroInt()
	}
	switch {
	case held.LT(owed):
		return types.ErrInvariantBroken.Wrapf(
			"dex module is short %s%s for pool %d: its reserve records %s and it holds %s",
			owed.Sub(held), denom, poolID, owed, held)
	case held.GT(owed):
		return types.ErrInvariantBroken.Wrapf(
			"dex module holds %s%s it cannot account for: pool %d records %s and it holds %s",
			held.Sub(owed), denom, poolID, owed, held)
	}
	return nil
}

// checkAuctionBidSolvency covers the one denom that is owed without a pool
// behind it: bids taken in an open window. O(1) — the auction is a singleton.
func (k Keeper) checkAuctionBidSolvency(ctx context.Context) error {
	a, err := k.getAuction(ctx)
	if errors.Is(err, types.ErrAuctionUnavailable) {
		return nil
	} else if err != nil {
		return err
	}
	if a.Status != types.AUCTION_STATUS_OPEN || a.BidDenom == "" || !a.TotalRaised.IsPositive() {
		return nil
	}
	held := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), a.BidDenom).Amount
	if !held.Equal(a.TotalRaised) {
		return types.ErrInvariantBroken.Wrapf(
			"dex holds %s%s in bids but the auction recorded %s", held, a.BidDenom, a.TotalRaised)
	}
	return nil
}

// AssertBoundedSolvency is the EndBlocker's check.
//
// Three parts, none of which grows with the pool count: the commingled ERTH
// balance, the spoke reserves of the pools this block wrote, and a rotating
// handful of pools nobody wrote. It clears the dirty set as it goes, so the
// cost of a quiet block is the rotation alone.
func (k Keeper) AssertBoundedSolvency(ctx context.Context) error {
	if err := k.checkErthSolvency(ctx); err != nil {
		return err
	}
	if err := k.checkAuctionBidSolvency(ctx); err != nil {
		return err
	}

	var dirty []uint64
	if err := k.DirtyPools.Walk(ctx, nil, func(id uint64) (bool, error) {
		dirty = append(dirty, id)
		return false, nil
	}); err != nil {
		return err
	}
	for _, id := range dirty {
		if err := k.checkPoolTokenSolvency(ctx, id); err != nil {
			return err
		}
		if err := k.DirtyPools.Remove(ctx, id); err != nil {
			return err
		}
	}

	return k.rotateSolvencyCheck(ctx)
}

// rotateSolvencyCheck re-checks a fixed number of pools per block, resuming
// where it left off and wrapping at the end. Bounded work, full coverage
// eventually — the backstop for a pool whose coins move without its record
// being written, which the dirty set alone would never notice.
func (k Keeper) rotateSolvencyCheck(ctx context.Context) error {
	cursor, err := k.SolvencyCursor.Get(ctx)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		cursor = 0
	}

	checked := 0
	next := cursor
	walk := func(start uint64) error {
		rng := new(collections.Range[uint64]).StartInclusive(start)
		return k.Pool.Walk(ctx, rng, func(id uint64, _ types.Pool) (bool, error) {
			if checked >= types.SolvencyRotationPerBlock {
				next = id
				return true, nil
			}
			if err := k.checkPoolTokenSolvency(ctx, id); err != nil {
				return true, err
			}
			checked++
			next = id + 1
			return false, nil
		})
	}
	if err := walk(cursor); err != nil {
		return err
	}
	// Ran off the end with budget to spare: wrap, so the cycle keeps turning.
	if checked < types.SolvencyRotationPerBlock && cursor != 0 {
		next = 0
		if err := walk(0); err != nil {
			return err
		}
	}
	return k.SolvencyCursor.Set(ctx, next)
}

// SolvencyProbe exposes the bounded check for tests, which use it alongside the
// exhaustive AssertInvariants to confirm the two agree.
func (k Keeper) SolvencyProbe(ctx context.Context) error { return k.AssertBoundedSolvency(ctx) }
