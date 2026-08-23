package keeper_test

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"cosmossdk.io/math"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

func bech32(t *testing.T, a sdk.AccAddress) string {
	t.Helper()
	s, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).BytesToString(a)
	require.NoError(t, err)
	return s
}

// A funded, quiet module balances: it owes its pool reserves and holds exactly them.
func TestBalancesHoldOnAFundedPool(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	rep, err := k.CheckBalances(ctx)
	require.NoError(t, err)
	require.False(t, rep.Broken(), "short: %s", rep.Short)
	require.NoError(t, k.AssertInvariants(ctx))
}

// The failure the invariant exists for: the module's records claim assets that
// are not on its balance. Nothing in the module can produce this today — which
// is the point of checking, because the next change might.
func TestBalanceCatchesAReserveWithNoCoinsBehindIt(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	// Inflate the recorded reserve without moving a coin.
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.AddAmount(math.NewInt(500_000))
	require.NoError(t, k.SetPool(ctx, 1, pool))

	rep, err := k.CheckBalances(ctx)
	require.NoError(t, err)
	require.True(t, rep.Broken())
	require.Equal(t, math.NewInt(500_000), rep.Short.AmountOf("uerth"))

	err = k.AssertHotInvariants(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "short")
}

// The direction that a "holds at least what it owes" check misses entirely, and
// the reason this one is an equality. Shrinking a reserve without burning the
// coins is what a retirement tranche or a fee deduction looks like when it
// forgets its second half: nothing is ever short, so nothing ever complains, and
// ERTH that was supposed to be destroyed quietly stays in existence.
func TestBalanceCatchesCoinsThatShouldHaveBeenBurned(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.SubAmount(math.NewInt(400_000))
	require.NoError(t, k.SetPool(ctx, 1, pool))

	rep, err := k.CheckBalances(ctx)
	require.NoError(t, err)
	require.True(t, rep.Broken())
	require.True(t, rep.Short.IsZero(), "nothing is short — that is the whole problem")
	require.Equal(t, math.NewInt(400_000), rep.Surplus.AmountOf("uerth"))

	err = k.AssertHotInvariants(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "cannot account for")
}

// Both auction earmarks are pre-funded from genesis and spoken for, so they are
// obligations from block zero — before any window opens and before any pool
// exists to hold them.
func TestBalanceCountsUnopenedAuctionEarmarks(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedAuction(t, k, ctx)

	owed, err := k.AssetObligations(ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(2*earmark), owed.AmountOf("uerth"),
		"a pending auction owes both earmarks")

	// Unfunded, it is short — the honest answer for a genesis that recorded the
	// earmarks without crediting the coins.
	rep, err := k.CheckBalances(ctx)
	require.NoError(t, err)
	require.True(t, rep.Broken())

	bank.fundModule(sdk.NewInt64Coin("uerth", 2*earmark))
	rep, err = k.CheckBalances(ctx)
	require.NoError(t, err)
	require.False(t, rep.Broken(), "short: %s", rep.Short)
}

// Bids sit on the module account until settlement pairs them with the pool
// earmark. Nothing else in the module accounts for them, so the obligation has
// to name them or every open auction reads as a surplus.
func TestBalanceCountsBidsInAnOpenWindow(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)
	bank.fundModule(sdk.NewInt64Coin("uerth", 2*earmark))

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)
	_, alice := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: alice, Amount: sdk.NewInt64Coin(bidDenom, 1_000),
	})
	require.NoError(t, err)

	owed, err := k.AssetObligations(ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000), owed.AmountOf(bidDenom),
		"the raise is held for the bidders until settlement")
	require.NoError(t, k.AssertInvariants(ctx))
}

// After settlement the pool earmark is inside the new pool's reserve, so
// counting it again would double it — and the module would read as insolvent for
// the rest of the chain's life.
func TestBalanceDoesNotDoubleCountAfterSettlement(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)
	bank.fundModule(sdk.NewInt64Coin("uerth", 2*earmark))

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)
	_, alice := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: alice, Amount: sdk.NewInt64Coin(bidDenom, 1_000),
	})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	owed, err := k.AssetObligations(ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(2*earmark), owed.AmountOf("uerth"),
		"one earmark is now the pool's reserve, the other is still owed to bidders")
	require.NoError(t, k.AssertInvariants(ctx))

	// Claiming moves ERTH out and reduces the obligation by the same amount.
	_, err = ms.ClaimLiquidityAuction(ctx, &types.MsgClaimLiquidityAuction{Bidder: alice})
	require.NoError(t, err)
	owed, err = k.AssetObligations(ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(earmark), owed.AmountOf("uerth"),
		"the bidders' earmark is claimed out; only the pool's reserve is still owed")
	require.NoError(t, k.AssertInvariants(ctx))
}

// The documented requirement in lp_rewards.go, enforced instead of asserted in
// prose. Drift here means the module either mints more than the allocation
// stream released or strands part of it, and neither is visible from outside.
func TestVolumeDenominatorMustMatchTheSumOfPools(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)
	seedFundedPool(t, k, ctx, bank, 2, 1_000_000, 1_000_000, 500)

	stored, summed, err := k.CheckVolumeAccounting(ctx)
	require.NoError(t, err)
	require.Equal(t, summed, stored)
	require.NoError(t, k.AssertInvariants(ctx))

	// Move the denominator without moving a pool.
	//
	// Checked through AssertInvariants rather than AssertHotInvariants: this
	// comparison is O(pools) and has no witness outside the module's own books,
	// so it left the per-block path when solvency was made bounded. It still runs
	// after every operation the tests perform, which is where a drift introduced
	// by a code change would surface.
	require.NoError(t, k.LpTotalVolume.Set(ctx, stored.AddRaw(250)))
	err = k.AssertInvariants(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "lp reward denominator")
}

// The protocol's own position and other people's in-flight withdrawals share one
// LP-denom balance. If it cannot cover both, one of them silently stops working.
func TestShareBackingCatchesAnOverCommittedPosition(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	shares := seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)

	rep, err := k.CheckShareBacking(ctx)
	require.NoError(t, err)
	require.False(t, rep.Broken(), "%v", rep.Problems)

	// Claim more of the pool than the module holds shares for.
	b, err := k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	b.TotalShares = shares.MulRaw(2)
	b.SharesRemaining = shares.MulRaw(2)
	require.NoError(t, k.PolBurns.Set(ctx, 1, b))

	rep, err = k.CheckShareBacking(ctx)
	require.NoError(t, err)
	require.True(t, rep.Broken())
	require.Contains(t, rep.Problems[0], "module holds")
}

// A retirement schedule that has retired more than it started with is arithmetic
// that has gone backwards; it would burn shares nobody has.
func TestShareBackingRejectsAnImpossibleSchedule(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	shares := seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)

	b, err := k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	b.SharesRemaining = shares.AddRaw(1) // more left than ever existed
	require.NoError(t, k.PolBurns.Set(ctx, 1, b))

	rep, err := k.CheckShareBacking(ctx)
	require.NoError(t, err)
	require.True(t, rep.Broken())
	require.Contains(t, rep.Problems[0], "shares left of a")
}

// The one that would actually catch a regression.
//
// Every path that moves the module's coins, driven in a random order against one
// funded pool, with every invariant asserted after each step. Individually each
// operation is covered elsewhere; what is not covered anywhere else is whether
// they still agree with each other after being interleaved — which is the only
// way the six writers to the reserves can be shown not to drift apart.
func TestInvariantsSurviveRandomisedOperations(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 7, 42, 1337} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			k, ctx, bank := initRewardFixture(t)
			ms := keeper.NewMsgServerImpl(k)

			seedFundedPool(t, k, ctx, bank, 1, 10_000_000, 10_000_000, 0)

			// The protocol owns the pool outright, on a retirement schedule.
			shares := math.NewInt(10_000_000)
			bank.setSupply(types.LPShareDenom(1), shares)
			bank.fundModule(sdk.NewCoin(types.LPShareDenom(1), shares))
			require.NoError(t, k.PolBurns.Set(ctx, 1, types.PolBurn{
				PoolId:          1,
				TotalShares:     shares,
				SharesRemaining: shares,
				StartTime:       ctx.BlockTime().Unix(),
				DurationSeconds: types.PolBurnSeconds,
				BurnToken:       false,
			}))

			trader := sdk.AccAddress("trader______________")
			lp := sdk.AccAddress("provider____________")
			lpStr := bech32(t, lp)

			// What the third-party provider actually holds, so a withdrawal only
			// ever escrows shares they really have — escrowing shares nobody
			// owns would manufacture an unbonding and break share backing for
			// reasons that have nothing to do with the code under test.
			lpShares := math.ZeroInt()

			for step := 0; step < 80; step++ {
				switch rng.Intn(6) {
				case 0: // swap token -> hub
					_, err := k.SwapExactIn(ctx, trader,
						sdk.NewInt64Coin("utok", int64(1+rng.Intn(50_000))), "uerth", math.ZeroInt())
					requireOKOrKnown(t, err)
				case 1: // swap hub -> token
					_, err := k.SwapExactIn(ctx, trader,
						sdk.NewInt64Coin("uerth", int64(1+rng.Intn(50_000))), "utok", math.ZeroInt())
					requireOKOrKnown(t, err)
				case 2: // a third party adds liquidity
					amt := int64(1 + rng.Intn(100_000))
					resp, err := ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
						Creator: lpStr, PoolId: 1,
						AmountA: sdk.NewInt64Coin("uerth", amt),
						AmountB: sdk.NewInt64Coin("utok", amt),
					})
					requireOKOrKnown(t, err)
					if err == nil {
						lpShares = lpShares.Add(resp.Shares.Amount)
					}
				case 5: // that provider starts withdrawing some of it
					if !lpShares.IsPositive() {
						continue
					}
					part := lpShares.QuoRaw(int64(1 + rng.Intn(4)))
					if !part.IsPositive() {
						continue
					}
					_, err := ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
						Creator: lpStr, PoolId: 1,
						Shares: sdk.NewCoin(types.LPShareDenom(1), part),
					})
					requireOKOrKnown(t, err)
					if err == nil {
						lpShares = lpShares.Sub(part)
					}
				case 3: // the LP-rewards allocation option pays out
					distributeLP(t, k, ctx, bank, math.NewInt(int64(1+rng.Intn(10_000))))
				case 4: // time passes, so the retirement schedule advances
					ctx = ctx.WithBlockTime(ctx.BlockTime().Add(
						time.Duration(1+rng.Intn(72)) * time.Hour))
					require.NoError(t, k.SweepMaturedUnbondings(ctx))
					require.NoError(t, k.BurnDuePol(ctx))
				}

				require.NoErrorf(t, k.AssertInvariants(ctx), "broken at step %d", step)
			}
		})
	}
}

// requireOKOrKnown accepts the errors an operation is allowed to fail with when
// it is handed random amounts — a dust trade that rounds to nothing, or a pool
// too thin for the size asked. Anything else is a real failure and must surface.
func requireOKOrKnown(t *testing.T, err error) {
	t.Helper()
	switch {
	case err == nil,
		isOneOf(err, types.ErrZeroShares, types.ErrInsufficientPool,
			types.ErrInvalidAmount, types.ErrSlippage):
		return
	default:
		t.Fatalf("unexpected error: %v", err)
	}
}

func isOneOf(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
