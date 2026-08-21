package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// seedPol writes a pool the protocol owns outright, plus the schedule retiring
// it: reserves in place, every share outstanding and none of them ever sent, so
// they sit on the module account the way genesis leaves them.
func seedPol(t *testing.T, k keeper.Keeper, ctx sdk.Context, bank *mintingBank,
	id uint64, erth, token int64, burnToken bool) math.Int {
	t.Helper()
	seedFundedPool(t, k, ctx, bank, id, erth, token, 0)

	shares := math.NewInt(erth).Mul(math.NewInt(token))
	shares = math.NewIntFromBigInt(shares.BigInt().Sqrt(shares.BigInt()))
	// The shares exist AND the module holds them: that is what makes the
	// position the protocol's, and what the retirement will burn.
	bank.setSupply(types.LPShareDenom(id), shares)
	bank.fundModule(sdk.NewCoin(types.LPShareDenom(id), shares))

	require.NoError(t, k.PolBurns.Set(ctx, id, types.PolBurn{
		PoolId:          id,
		TotalShares:     shares,
		SharesRemaining: shares,
		StartTime:       ctx.BlockTime().Unix(),
		DurationSeconds: types.PolBurnSeconds,
		BurnToken:       burnToken,
	}))
	return shares
}

// A schedule with start_time 0 is what a genesis file has to write, because it
// cannot know the chain's first block time. Reading that zero as the unix epoch
// would put the end date thirty-five years in the past and retire the entire
// position in block one, so the first block that sees the entry has to anchor it
// instead — and retire nothing while doing so.
func TestPolBurnAnchorsZeroStartOnFirstBlock(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	shares := seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)
	b, err := k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	b.StartTime = 0
	require.NoError(t, k.PolBurns.Set(ctx, 1, b))

	require.NoError(t, k.BurnDuePol(ctx))

	b, err = k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Unix(), b.StartTime, "zero start should anchor on this block")
	require.Equal(t, shares, b.SharesRemaining, "anchoring must not retire anything")
	require.True(t, bank.burned.IsZero())
}

// The genesis ANML/ERTH schedule: both assets are the chain's own, so a retired
// slice burns both sides. Both reserves shrink by the same fraction, which is
// what makes retirement invisible to the pool's price — an LP or a trader sees
// a thinner book, never a different quote.
func TestPolBurnBothSidesLeavesPriceUnchanged(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	shares := seedPol(t, k, ctx, bank, 1, 4_000_000, 1_000_000, true)

	// A tenth of the way through the schedule.
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(types.PolBurnSeconds/10) * time.Second))
	require.NoError(t, k.BurnDuePol(ctx))

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(3_600_000), pool.ReserveErth.Amount)
	require.Equal(t, math.NewInt(900_000), pool.ReserveToken.Amount)
	require.Equal(t, math.NewInt(4), pool.ReserveErth.Amount.Quo(pool.ReserveToken.Amount),
		"burning both sides must not move the price")

	require.Equal(t, math.NewInt(400_000), bank.burned.AmountOf("uerth"))
	require.Equal(t, math.NewInt(100_000), bank.burned.AmountOf("utok"))

	b, err := k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, shares.Sub(shares.QuoRaw(10)), b.SharesRemaining)
	require.Equal(t, shares.QuoRaw(10), bank.burned.AmountOf(types.LPShareDenom(1)),
		"the shares behind the burned assets must be retired too")
}

// The auction pool's schedule. Its spoke side is a bridged asset the chain
// cannot recreate, so only the ERTH is destroyed and the spoke asset is left in
// the reserve. That walks the pool's price up on purpose: it is what makes
// arbitrageurs sell ERTH in and take the spoke asset out, so the auction's
// proceeds end up buying ERTH that later tranches burn.
func TestPolBurnErthOnlyLeavesTheSpokeAsset(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPol(t, k, ctx, bank, 1, 4_000_000, 1_000_000, false)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(types.PolBurnSeconds/10) * time.Second))
	require.NoError(t, k.BurnDuePol(ctx))

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(3_600_000), pool.ReserveErth.Amount)
	require.Equal(t, math.NewInt(1_000_000), pool.ReserveToken.Amount,
		"the spoke asset must stay in the pool")
	require.Equal(t, math.NewInt(400_000), bank.burned.AmountOf("uerth"))
	require.True(t, bank.burned.AmountOf("utok").IsZero())
}

// The target is recomputed from the clock every block rather than accumulated,
// so a chain that halts does not push its end date out by the length of the
// halt: it catches up on the block it resumes on, and truncation never
// compounds across the millions of blocks in ten years.
func TestPolBurnCatchesUpAfterAHalt(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	start := ctx.BlockTime()
	seedPol(t, k, ctx, bank, 1, 10_000_000, 10_000_000, true)

	// Tick along for a while, then jump as though the chain had stopped.
	for i := 1; i <= 5; i++ {
		ctx = ctx.WithBlockTime(start.Add(time.Duration(int64(i)*types.PolBurnSeconds/100) * time.Second))
		require.NoError(t, k.BurnDuePol(ctx))
	}
	ctx = ctx.WithBlockTime(start.Add(time.Duration(types.PolBurnSeconds/2) * time.Second))
	require.NoError(t, k.BurnDuePol(ctx))

	b, err := k.PolBurns.Get(ctx, 1)
	require.NoError(t, err)
	retired := b.TotalShares.Sub(b.SharesRemaining)
	require.Equal(t, b.TotalShares.QuoRaw(2), retired,
		"half the schedule elapsed, so half the position should be retired regardless of the halt")
}

// The end of the line. The position goes to zero — not to a floor — because the
// protocol getting out of the way completely is the point: LPs take the book
// over, and a residual position nobody can manage would be the same mismatch in
// miniature. The finished schedule is deleted rather than left for the
// EndBlocker to reload every block forever.
func TestPolBurnRetiresTheWholePositionAndStops(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	start := ctx.BlockTime()
	shares := seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)

	ctx = ctx.WithBlockTime(start.Add(time.Duration(types.PolBurnSeconds+1) * time.Second))
	require.NoError(t, k.BurnDuePol(ctx))

	require.Equal(t, shares, bank.burned.AmountOf(types.LPShareDenom(1)))
	require.Equal(t, math.NewInt(1_000_000), bank.burned.AmountOf("uerth"))
	require.Equal(t, math.NewInt(1_000_000), bank.burned.AmountOf("utok"))

	has, err := k.PolBurns.Has(ctx, 1)
	require.NoError(t, err)
	require.False(t, has, "a finished schedule should be deleted")

	// Running again is a no-op rather than an error or a second burn.
	before := bank.burned
	require.NoError(t, k.BurnDuePol(ctx))
	require.Equal(t, before, bank.burned)
}

// LP rewards accrue into the reserve, so the protocol's position keeps earning
// ERTH the whole time it is being retired. Those earnings have to be settled
// into the reserve before a slice is priced against it, or the position is
// retired at less than it is worth and the ERTH it earned is left behind for
// whoever is still holding shares at the end.
//
// One day of the schedule, not a decade: a pool left untouched past the volume
// window decays to zero weight and collects nothing, so a ten-year jump would
// test the dormancy rule rather than this one.
func TestPolBurnClawsBackTheRewardsItEarned(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	start := ctx.BlockTime()
	seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)

	// Give the pool volume so it can collect, then hand the dex a reward.
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.Volume = math.NewInt(1_000)
	require.NoError(t, k.Pool.Set(ctx, 1, pool))
	require.NoError(t, k.LpTotalVolume.Set(ctx, math.NewInt(1_000)))
	_, err = k.DistributeLPRewards(ctx, math.NewInt(100_000))
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(start.Add(24 * time.Hour))
	require.NoError(t, k.BurnDuePol(ctx))

	// The pool was seeded 1:1, so had the slice been priced before the reward
	// settled both legs would have burned the same amount. The ERTH leg is
	// larger by exactly the reward's share of the slice.
	burnedErth := bank.burned.AmountOf("uerth")
	burnedToken := bank.burned.AmountOf("utok")
	require.True(t, burnedErth.GT(burnedToken),
		"the erth leg should be priced against a reserve that already holds the reward")

	// A day of a ten-year schedule is 1/3652.5 of the position: 273 of 1,000,000
	// shares. The reward that settled in is 85,700 rather than the full 100,000,
	// because a day's decay takes the pool's volume to six sevenths first.
	require.Equal(t, math.NewInt(273), bank.burned.AmountOf(types.LPShareDenom(1)))
	require.Equal(t, math.NewInt(273), burnedToken)
	require.Equal(t, math.NewInt(296), burnedErth)
	pool, err = k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000+85_700-296), pool.ReserveErth.Amount)
}

// Third-party shares are in the denominator, so retirement prices the protocol's
// slice at the fraction of the pool it actually owns — and a provider who joined
// is left with exactly the assets their shares are worth, untouched.
func TestPolBurnPricesAgainstThirdPartyShares(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	start := ctx.BlockTime()
	polShares := seedPol(t, k, ctx, bank, 1, 1_000_000, 1_000_000, true)

	// A provider matches the pool: same shares again, reserves doubled.
	bank.setSupply(types.LPShareDenom(1), polShares)
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.AddAmount(math.NewInt(1_000_000))
	pool.ReserveToken = pool.ReserveToken.AddAmount(math.NewInt(1_000_000))
	require.NoError(t, k.Pool.Set(ctx, 1, pool))

	ctx = ctx.WithBlockTime(start.Add(time.Duration(types.PolBurnSeconds) * time.Second))
	require.NoError(t, k.BurnDuePol(ctx))

	// The protocol owned half the pool, so half of each reserve is burned and
	// the provider's half is still standing.
	require.Equal(t, math.NewInt(1_000_000), bank.burned.AmountOf("uerth"))
	require.Equal(t, math.NewInt(1_000_000), bank.burned.AmountOf("utok"))
	pool, err = k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000), pool.ReserveErth.Amount)
	require.Equal(t, math.NewInt(1_000_000), pool.ReserveToken.Amount)
	require.Equal(t, polShares, bank.GetSupply(ctx, types.LPShareDenom(1)).Amount,
		"only the protocol's shares should be retired")
}

// Settling the auction has to register the pool it just opened, or two thirds of
// the pre-mine would sit in a position nothing ever retires. Its ten years run
// from the day the pool opens, not from block zero: governance chooses when to
// hold the auction, and a schedule already part-spent before there was anything
// to retire would dump the position the moment it existed.
func TestAuctionSettlementSchedulesItsOwnRetirement(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)
	_, alice := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: alice, Amount: sdk.NewInt64Coin(bidDenom, 1_000),
	})
	require.NoError(t, err)

	settleAt := ctx.BlockTime().Add(2 * time.Hour)
	ctx = ctx.WithBlockTime(settleAt)
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	b, err := k.PolBurns.Get(ctx, a.PoolId)
	require.NoError(t, err)
	require.Equal(t, settleAt.Unix(), b.StartTime, "the clock starts when the pool opens")
	require.Equal(t, int64(types.PolBurnSeconds), b.DurationSeconds)
	require.False(t, b.BurnToken, "the auction's spoke side is bridged and must not be burned")
	require.Equal(t, b.TotalShares, b.SharesRemaining)
}
