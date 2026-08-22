package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// capFor is the ceiling the keeper applies: the per-day multiple times the
// window the accumulator spans, against the pool's ERTH reserve.
func capFor(reserveErth int64) math.Int {
	return math.NewInt(reserveErth).
		MulRaw(types.DefaultVolumeDepthCapPerDay).
		MulRaw(types.VolumeWindowDays)
}

// roundTrip wash-trades against a pool: ERTH in, then the token straight back
// out. It is the cheapest way to manufacture volume, because half of each fee
// returns to the pool and a sole LP recovers it.
func roundTrip(t *testing.T, k keeper.Keeper, ctx sdk.Context, erthIn int64) {
	t.Helper()
	// No funding here: the stub bank credits the module account on the way in,
	// the same as the real one, so pre-funding would book the input twice and
	// break the solvency invariant these tests assert.
	trader := sdk.AccAddress("washer______________")
	tok, err := k.SwapExactIn(ctx, trader, sdk.NewInt64Coin("uerth", erthIn), "utok", math.ZeroInt())
	require.NoError(t, err)
	_, err = k.SwapExactIn(ctx, trader, tok, "uerth", math.ZeroInt())
	require.NoError(t, err)
}

// TestVolumeCapBoundsWashTrading is the point of the cap. Manufacturing volume
// against a shallow pool has to stop earning more weight once the volume passes
// what the pool's own depth justifies.
func TestVolumeCapBoundsWashTrading(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	for i := 0; i < 60; i++ {
		roundTrip(t, k, ctx, 500_000)
	}

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	ceiling := capFor(pool.ReserveErth.Amount.Int64())
	require.True(t, pool.Volume.LTE(ceiling),
		"counted volume %s exceeded the depth cap %s", pool.Volume, ceiling)
	require.Equal(t, ceiling, pool.Volume, "sustained wash trading should sit exactly at the cap")
	require.NoError(t, k.AssertInvariants(ctx))
}

// TestVolumeCapLeavesHonestPoolsAlone: a pool trading at a realistic fraction of
// its depth must be untouched, or the cap is taxing the behaviour it wants.
func TestVolumeCapLeavesHonestPoolsAlone(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 10_000_000, 10_000_000, 0)

	// ~10% of the ERTH reserve traded in a day, well inside a 2x/day allowance.
	roundTrip(t, k, ctx, 500_000)
	roundTrip(t, k, ctx, 500_000)

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, pool.Volume.LT(capFor(pool.ReserveErth.Amount.Int64())),
		"an honest pool should sit below the cap, got %s", pool.Volume)
	require.True(t, pool.Volume.IsPositive(), "honest volume must still count")
	require.NoError(t, k.AssertInvariants(ctx))
}

// TestDepthRaisesTheCeiling states the incentive the cap creates: the way to
// earn more weight is to bring more depth. This is what turns the wash-trader
// into the liquidity provider the reward was meant to attract.
func TestDepthRaisesTheCeiling(t *testing.T) {
	shallow := capFor(1_000_000)
	deep := capFor(10_000_000)
	require.True(t, deep.GT(shallow),
		"a deeper pool must be allowed more counted volume, %s vs %s", deep, shallow)
	require.Equal(t, shallow.MulRaw(10), deep, "the ceiling should track depth linearly")
}

// TestVolumeCapFollowsDepthDown closes the two-step version of the same trade:
// build weight while the pool is deep, pull the liquidity out, keep earning
// against depth that is no longer there.
func TestVolumeCapFollowsDepthDown(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 10_000_000, 10_000_000, 0)

	for i := 0; i < 40; i++ {
		roundTrip(t, k, ctx, 2_000_000)
	}
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	atDepth := pool.Volume
	require.True(t, atDepth.IsPositive())

	// Depth collapses. Written straight into the pool rather than withdrawn
	// through the unbonding queue, because what is under test is the cap
	// re-applying to a reduced reserve, not how the reserve got reduced. That
	// leaves the module's coin balance deliberately out of step with its
	// records, so this test does not assert solvency — the cases above and below
	// it do, through paths that keep the two together.
	pool.ReserveErth = sdk.NewInt64Coin("uerth", 100_000)
	require.NoError(t, k.Pool.Set(ctx, 1, pool))
	roundTrip(t, k, ctx, 1_000)

	pool, err = k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, pool.Volume.LTE(capFor(pool.ReserveErth.Amount.Int64())),
		"volume %s outlived the depth that justified it (cap %s)",
		pool.Volume, capFor(pool.ReserveErth.Amount.Int64()))
	require.True(t, pool.Volume.LT(atDepth), "counted volume should fall with depth")
}

// TestCappingKeepsTheDenominatorInStep guards the accounting the cap runs
// through. LpTotalVolume must equal the sum of stored pool volumes; clamping a
// pool down has to take the same amount out of the total, or the module mints
// against volume no pool is holding.
func TestCappingKeepsTheDenominatorInStep(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	// Seeded far above what this depth can justify, as a chain upgrading into
	// the cap with volume already stored would be.
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 900_000_000)

	require.NoError(t, k.AssertInvariants(ctx))

	roundTrip(t, k, ctx, 1_000) // any touch re-applies the cap

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	total, err := k.LpTotalVolume.Get(ctx)
	require.NoError(t, err)

	require.Equal(t, capFor(pool.ReserveErth.Amount.Int64()), pool.Volume,
		"stored volume should be clamped to the cap on first touch")
	require.Equal(t, pool.Volume, total,
		"the global denominator must follow the clamp down, got %s for a pool holding %s",
		total, pool.Volume)
	require.NoError(t, k.AssertInvariants(ctx))
}
