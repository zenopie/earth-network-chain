package keeper

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/dex/types"
)

// What the dex module must be able to pay, and the check that it can.
//
// The dex module account holds the whole pre-mine. Every pool reserve, both
// liquidity-auction earmarks, every bid placed in an open window and every LP
// share escrowed against a withdrawal in flight are one bank balance, and six
// separate paths move it: swap fee burns, LP unbonding payouts, LP reward
// minting, AddLiquidity deposits, the personhood buyback, and the retirement of
// the protocol's own liquidity. Each is individually correct. Nothing until now
// checked that they still agree with each other.
//
// That is the failure worth catching. An arithmetic slip in any one of them
// leaves the module's records claiming assets it no longer holds, and the loss
// surfaces later as a withdrawal that cannot be paid — by which point the chain
// has been wrong for an unknown number of blocks and there is no way back.
//
// The check is an exact equality, not "holds at least what it owes". Both
// directions are bugs and only one of them is obvious. A shortfall is a
// withdrawal that will not be payable. A *surplus* is coins the module was
// supposed to have destroyed and did not — a retirement tranche that shrank the
// reserve without burning, a fee that was deducted but stayed — which is silent
// forever, because nothing ever tries to spend it.
//
// Equality is only enforceable because the module account is blocked from
// receiving ordinary transfers (app/app_config.go). Without that, anyone could
// halt the chain with a MsgSend, and the check would have to weaken to "at
// least" and stop catching the second class entirely.

// BalanceReport is what the module owes, what it holds, and how the two differ.
// Denominated per coin because the answers differ: a gap in the hub denom is the
// pre-mine, in a spoke denom it is one pool.
type BalanceReport struct {
	Owed sdk.Coins
	Held sdk.Coins
	// Short is what the module owes and cannot pay.
	Short sdk.Coins
	// Surplus is what it holds and cannot account for — coins that should have
	// been burned or paid out and were not.
	Surplus sdk.Coins
}

func (r BalanceReport) Broken() bool { return !r.Short.IsZero() || !r.Surplus.IsZero() }

// AssetObligations returns every asset the dex module has promised to someone.
//
// It is O(pools), with no walk of the unbonding queue, because it is called
// every block. In-flight unbondings need no entry: their assets are still in the
// pool reserves — which is the point of escrowing the shares rather than burning
// them — and their escrowed LP shares are checked separately by
// ShareBackingReport, which is not on the hot path.
func (k Keeper) AssetObligations(ctx context.Context) (sdk.Coins, error) {
	owed := sdk.NewCoins()
	if err := k.Pool.Walk(ctx, nil, func(_ uint64, p types.Pool) (bool, error) {
		// A pool's two reserves are coins the module is holding for its LPs.
		if p.ReserveErth.Amount.IsPositive() {
			owed = owed.Add(p.ReserveErth)
		}
		if p.ReserveToken.Amount.IsPositive() {
			owed = owed.Add(p.ReserveToken)
		}
		return false, nil
	}); err != nil {
		return nil, err
	}

	a, err := k.getAuction(ctx)
	switch {
	case errors.Is(err, types.ErrAuctionUnavailable):
		// No auction configured; nothing owed on its account.
	case err != nil:
		return nil, err
	default:
		switch a.Status {
		case types.AUCTION_STATUS_SETTLED:
			// erth_for_pool became the new pool's reserve and is already counted
			// above. What is still owed is the part of the bidders' earmark
			// nobody has claimed yet. Claimed is incremented by exactly the
			// amount each claim sends, so the difference is exact — including
			// the truncation dust that no bidder will ever be able to take.
			if rem := a.ErthForBidders.Amount.Sub(a.Claimed); rem.IsPositive() {
				owed = owed.Add(sdk.NewCoin(a.ErthForBidders.Denom, rem))
			}
		default:
			// PENDING or OPEN: both earmarks are still sitting on the module
			// account, pre-funded from genesis and spoken for.
			if a.ErthForBidders.Amount.IsPositive() {
				owed = owed.Add(a.ErthForBidders)
			}
			if a.ErthForPool.Amount.IsPositive() {
				owed = owed.Add(a.ErthForPool)
			}
			// Bids in an open window are held until settlement pairs them with
			// the pool earmark. Nothing else in the module accounts for them.
			if a.Status == types.AUCTION_STATUS_OPEN && a.TotalRaised.IsPositive() && a.BidDenom != "" {
				owed = owed.Add(sdk.NewCoin(a.BidDenom, a.TotalRaised))
			}
		}
	}

	return owed, nil
}

// CheckBalances compares what the module owes against what it holds, in both
// directions, across every asset denom on the account.
//
// LP share denoms are excluded: they are not assets the module owes anyone, they
// are claims on assets already counted in the pool reserves, and their backing is
// CheckShareBacking's job — which needs the unbonding queue and so cannot run on
// the per-block path.
func (k Keeper) CheckBalances(ctx context.Context) (BalanceReport, error) {
	owed, err := k.AssetObligations(ctx)
	if err != nil {
		return BalanceReport{}, err
	}

	moduleAddr := authtypes.NewModuleAddress(types.ModuleName)

	held := sdk.NewCoins()
	for _, c := range k.bankKeeper.GetAllBalances(ctx, moduleAddr) {
		if types.IsLPShareDenom(c.Denom) {
			continue
		}
		held = held.Add(c)
	}

	short, surplus := sdk.NewCoins(), sdk.NewCoins()
	for _, c := range owed {
		if bal := held.AmountOf(c.Denom); bal.LT(c.Amount) {
			short = short.Add(sdk.NewCoin(c.Denom, c.Amount.Sub(bal)))
		}
	}
	for _, c := range held {
		if want := owed.AmountOf(c.Denom); c.Amount.GT(want) {
			surplus = surplus.Add(sdk.NewCoin(c.Denom, c.Amount.Sub(want)))
		}
	}

	return BalanceReport{Owed: owed, Held: held, Short: short, Surplus: surplus}, nil
}

// CheckVolumeAccounting verifies the LP reward denominator.
//
// lp_rewards.go states this as a requirement in prose: LpTotalVolume must equal
// the sum of every pool's stored Volume, because DistributeLPRewards reports
// back delta*LpTotalVolume as the amount it handed out while the pools between
// them claim delta*sum(Volume). Drift in either direction is a real loss — the
// module either mints more ERTH than the allocation stream released, or strands
// part of what it did release — and it is silent, which is why it is worth a
// check rather than a comment.
func (k Keeper) CheckVolumeAccounting(ctx context.Context) (stored, summed math.Int, err error) {
	stored, err = k.getLpTotalVolume(ctx)
	if err != nil {
		return math.Int{}, math.Int{}, err
	}
	summed = math.ZeroInt()
	if err := k.Pool.Walk(ctx, nil, func(_ uint64, p types.Pool) (bool, error) {
		if !p.Volume.IsNil() {
			summed = summed.Add(p.Volume)
		}
		return false, nil
	}); err != nil {
		return math.Int{}, math.Int{}, err
	}
	return stored, summed, nil
}

// ShareBackingReport is the LP-share side of solvency: the shares the module is
// holding on other people's behalf, against the shares that actually exist.
type ShareBackingReport struct {
	Problems []string
}

func (r ShareBackingReport) Broken() bool { return len(r.Problems) > 0 }

// CheckShareBacking verifies, per pool, that the LP shares the module is
// committed to are shares it actually holds and that actually exist.
//
// Two distinct claims share the module's LP-denom balance: shares escrowed
// against withdrawals in flight, which must be burnable when they mature, and
// the protocol's own position, which must be burnable on its retirement
// schedule. If the balance cannot cover both, one of them silently stops
// working — an unbonding that can never pay out, or a retirement that halts the
// EndBlocker every block.
//
// This walks the unbonding queue, so it is O(withdrawals in flight) and is
// deliberately kept off the per-block path. Tests run it after every operation;
// operators can run it against a node.
func (k Keeper) CheckShareBacking(ctx context.Context) (ShareBackingReport, error) {
	var rep ShareBackingReport

	escrowed := map[uint64]math.Int{}
	if err := k.LpUnbondings.Walk(ctx, nil,
		func(_ collections.Triple[int64, uint64, []byte], u types.LpUnbonding) (bool, error) {
			cur, ok := escrowed[u.PoolId]
			if !ok {
				cur = math.ZeroInt()
			}
			escrowed[u.PoolId] = cur.Add(u.Shares.Amount)
			return false, nil
		}); err != nil {
		return rep, err
	}

	pol := map[uint64]math.Int{}
	if err := k.PolBurns.Walk(ctx, nil, func(id uint64, b types.PolBurn) (bool, error) {
		if b.SharesRemaining.IsNegative() {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("pool %d: retirement schedule has negative shares_remaining %s",
					id, b.SharesRemaining))
		}
		if b.SharesRemaining.GT(b.TotalShares) {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("pool %d: retirement schedule has %s shares left of a %s position",
					id, b.SharesRemaining, b.TotalShares))
		}
		pol[id] = b.SharesRemaining
		return false, nil
	}); err != nil {
		return rep, err
	}

	moduleAddr := authtypes.NewModuleAddress(types.ModuleName)

	pools := make([]uint64, 0, len(escrowed)+len(pol))
	for id := range escrowed {
		pools = append(pools, id)
	}
	for id := range pol {
		if _, dup := escrowed[id]; !dup {
			pools = append(pools, id)
		}
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })

	for _, id := range pools {
		denom := types.LPShareDenom(id)

		claimed := math.ZeroInt()
		if v, ok := escrowed[id]; ok {
			claimed = claimed.Add(v)
		}
		if v, ok := pol[id]; ok && v.IsPositive() {
			claimed = claimed.Add(v)
		}

		if bal := k.bankKeeper.GetBalance(ctx, moduleAddr, denom); bal.Amount.LT(claimed) {
			rep.Problems = append(rep.Problems, fmt.Sprintf(
				"pool %d: module holds %s %s but owes %s (escrowed withdrawals plus its own position)",
				id, bal.Amount, denom, claimed))
		}
		if supply := k.totalShares(ctx, id).Amount; supply.LT(claimed) {
			rep.Problems = append(rep.Problems, fmt.Sprintf(
				"pool %d: only %s %s exist but %s are spoken for", id, supply, denom, claimed))
		}
	}

	return rep, nil
}

// AssertInvariants runs every check and returns the first breach as an error.
// Tests use it after each operation; nothing calls it per block, because
// CheckShareBacking walks the unbonding queue.
func (k Keeper) AssertInvariants(ctx context.Context) error {
	if err := k.assertHotInvariants(ctx); err != nil {
		return err
	}
	rep, err := k.CheckShareBacking(ctx)
	if err != nil {
		return err
	}
	if rep.Broken() {
		return types.ErrInvariantBroken.Wrap(rep.Problems[0])
	}
	return nil
}

// assertHotInvariants is the subset cheap enough to run in the EndBlocker: both
// checks are O(pools) and neither touches the unbonding queue.
func (k Keeper) assertHotInvariants(ctx context.Context) error {
	rep, err := k.CheckBalances(ctx)
	if err != nil {
		return err
	}
	switch {
	case !rep.Short.IsZero():
		return types.ErrInvariantBroken.Wrapf(
			"dex module is short %s: it owes %s and holds %s", rep.Short, rep.Owed, rep.Held)
	case !rep.Surplus.IsZero():
		return types.ErrInvariantBroken.Wrapf(
			"dex module holds %s it cannot account for: it owes %s and holds %s",
			rep.Surplus, rep.Owed, rep.Held)
	}

	stored, summed, err := k.CheckVolumeAccounting(ctx)
	if err != nil {
		return err
	}
	if !stored.Equal(summed) {
		return types.ErrInvariantBroken.Wrapf(
			"lp reward denominator is %s but the pools' volume sums to %s", stored, summed)
	}
	return nil
}

// AssertHotInvariants is the EndBlocker's check. Returning an error from an
// EndBlocker halts the chain, which is the intended outcome: the module is
// already wrong at that point, and every further block spends assets it has
// mispriced. Halting is recoverable by an upgrade; a slow silent drain of the
// pre-mine is not.
func (k Keeper) AssertHotInvariants(ctx context.Context) error {
	if err := k.assertHotInvariants(ctx); err != nil {
		sdk.UnwrapSDKContext(ctx).Logger().Error(
			"dex invariant broken — halting", "err", err,
			"height", sdk.UnwrapSDKContext(ctx).BlockHeight())
		return err
	}
	return nil
}
