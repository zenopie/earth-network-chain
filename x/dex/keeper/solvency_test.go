package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/types"
)

// TestSolvencyCostIsFlatInPoolCount is the point of the change. A chain that
// expects a token per project cannot afford a per-block check whose cost grows
// with the number of pools, and the old one walked every pool twice — once for
// what was owed and again, through GetAllBalances, for what was held.
//
// Measured in store reads rather than in time, which is the thing that actually
// scales and the thing a benchmark on a fast laptop would hide.
func TestSolvencyCostIsFlatInPoolCount(t *testing.T) {
	cost := func(pools int) uint64 {
		k, ctx, bank := initRewardFixture(t)
		for i := 1; i <= pools; i++ {
			seedFundedPool(t, k, ctx, bank, uint64(i), 1_000_000, 1_000_000, 0)
		}
		// Seeding marked every pool dirty. Drain that the way a block boundary
		// would, so what is measured next is a quiet block rather than a backlog.
		for i := 0; i < pools+2; i++ {
			require.NoError(t, k.AssertBoundedSolvency(ctx))
		}

		gm := storetypes.NewInfiniteGasMeter()
		metered := ctx.WithGasMeter(gm)
		require.NoError(t, k.AssertBoundedSolvency(metered))
		return gm.GasConsumed()
	}

	small := cost(5)
	large := cost(200)

	// Forty times the pools must not mean anything like forty times the work.
	// The allowance covers the fixed rotation and per-call overhead, not growth.
	require.Less(t, large, small*3,
		"solvency cost tracks pool count: %d gas at 5 pools, %d at 200", small, large)
	t.Logf("quiet-block solvency cost: %d gas at 5 pools, %d at 200", small, large)
}

// TestBoundedCheckStillCatchesAShortfall: the whole reason the check exists is
// a reserve with no coins behind it. Making it cheap must not make it blind.
func TestBoundedCheckStillCatchesAShortfall(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	require.NoError(t, k.AssertBoundedSolvency(ctx))

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.AddAmount(math.NewInt(500_000))
	require.NoError(t, k.SetPool(ctx, 1, pool))

	err = k.AssertBoundedSolvency(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "short")
}

// TestBoundedCheckStillCatchesCoinsThatShouldHaveBeenBurned: the direction a
// "holds at least what it owes" check misses. Shrinking a reserve without
// burning the coins leaves nothing short, so nothing complains, and ERTH that
// should have been destroyed quietly survives.
func TestBoundedCheckStillCatchesCoinsThatShouldHaveBeenBurned(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	require.NoError(t, k.AssertBoundedSolvency(ctx))

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.SubAmount(math.NewInt(400_000))
	require.NoError(t, k.SetPool(ctx, 1, pool))

	err = k.AssertBoundedSolvency(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "cannot account for")
}

// TestSpokeTokenShortfallIsCaught covers the per-denom half. ERTH is commingled
// and tracked by a running total; every other denom belongs to exactly one pool,
// so its own reserve is the entire obligation.
func TestSpokeTokenShortfallIsCaught(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveToken = pool.ReserveToken.AddAmount(math.NewInt(250_000))
	require.NoError(t, k.SetPool(ctx, 1, pool))

	err = k.AssertBoundedSolvency(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "short")
}

// TestRotationCatchesAnUntouchedPool is the backstop. The dirty set covers
// anything a block wrote; this covers the case it cannot see — a pool whose
// coins move without its record being written, which would never be marked and
// so would never be checked.
func TestRotationCatchesAnUntouchedPool(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	for i := 1; i <= 3; i++ {
		seedFundedPool(t, k, ctx, bank, uint64(i), 1_000_000, 1_000_000, 0)
	}
	require.NoError(t, k.AssertBoundedSolvency(ctx)) // clears the dirty set

	// Coins vanish from a pool nobody is going to write.
	bank.debit(sdk.NewCoins(sdk.NewInt64Coin(seedTokenDenom(3), 100_000)))

	// The rotation reaches it without anything marking it dirty.
	var err error
	for i := 0; i < 5 && err == nil; i++ {
		err = k.AssertBoundedSolvency(ctx)
	}
	require.ErrorIs(t, err, types.ErrInvariantBroken)
}

// TestBypassingSetPoolIsCaught guards the funnel the running total depends on.
// A pool written directly leaves total_pool_erth describing a set of pools it no
// longer sums, and the per-block check compares that total to the bank — so the
// drift would be reported as the module being short rather than as the bug it is.
func TestBypassingSetPoolIsCaught(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)

	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	pool.ReserveErth = pool.ReserveErth.AddAmount(math.NewInt(1))
	require.NoError(t, k.Pool.Set(ctx, 1, pool)) // deliberately around SetPool

	err = k.CheckErthTotalAccounting(ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "SetPool")
}
