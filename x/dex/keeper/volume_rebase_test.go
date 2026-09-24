package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/types"
)

// The rebase must be invisible to LPs: the same trades and the same rewards,
// with or without a rebase in the middle, credit each pool the same ERTH.
//
// Each run seeds two pools, records volume 3:1 at an index already at the
// rebase threshold, distributes rewards that stay unsettled, then (in one run
// only) rebases, distributes again and settles both pools. Any drift beyond a
// few uerth of truncation means the rebase moved someone's share or dropped an
// unsettled reward.
func TestVolumeRebaseLeavesRewardsUnchanged(t *testing.T) {
	reserves := func(rebase bool) (math.Int, math.Int) {
		k, ctx, bank := initRewardFixture(t)
		seedPool(t, k, ctx, 1, 100_000_000_000_000, 100_000_000_000_000, 0)
		seedPool(t, k, ctx, 2, 100_000_000_000_000, 100_000_000_000_000, 0)

		atThreshold := math.NewIntWithDecimal(1, 18).MulRaw(types.VolumeIndexRebaseAt)
		require.NoError(t, k.VolumeIndex.Set(ctx, atThreshold))
		for id, amount := range map[uint64]int64{1: 3_000_000, 2: 1_000_000} {
			p, err := k.Pool.Get(ctx, id)
			require.NoError(t, err)
			require.NoError(t, k.ApplyVolumeForTest(ctx, &p, math.NewInt(amount)))
			require.NoError(t, k.SetPool(ctx, id, p))
		}

		distributeLP(t, k, ctx, bank, math.NewInt(5_000_000))

		if rebase {
			require.NoError(t, k.RebaseVolumeIndex(ctx))

			idx, err := k.VolumeIndex.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, atThreshold.QuoRaw(types.VolumeIndexRebaseFactor), idx)
			stored, summed, err := k.CheckVolumeAccounting(ctx)
			require.NoError(t, err)
			require.Equal(t, summed, stored, "LpTotalVolume must stay the sum of the pools")
			p1, err := k.Pool.Get(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, math.NewInt(3_000_000), p1.VolumeWeight)
		}

		distributeLP(t, k, ctx, bank, math.NewInt(5_000_000))
		var out [2]math.Int
		for i, id := range []uint64{1, 2} {
			p, err := k.Pool.Get(ctx, id)
			require.NoError(t, err)
			require.NoError(t, k.SettleForTest(ctx, id, &p))
			out[i] = p.ReserveErth.Amount
		}
		pending, err := k.PendingLpRewards.Get(ctx)
		require.NoError(t, err)
		require.False(t, pending.IsNegative(), "pending went negative: %s", pending)
		return out[0], out[1]
	}

	base1, base2 := reserves(false)
	got1, got2 := reserves(true)
	require.True(t, got1.Sub(base1).Abs().LTE(math.NewInt(2)), "pool 1: %s vs %s", got1, base1)
	require.True(t, got2.Sub(base2).Abs().LTE(math.NewInt(2)), "pool 2: %s vs %s", got2, base2)
}

// Below the threshold the rebase does nothing at all.
func TestVolumeRebaseWaitsForTheThreshold(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000_000, 1_000_000_000, 0)
	below := math.NewIntWithDecimal(1, 18).MulRaw(types.VolumeIndexRebaseAt).SubRaw(1)
	require.NoError(t, k.VolumeIndex.Set(ctx, below))
	p, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, k.ApplyVolumeForTest(ctx, &p, math.NewInt(1_000)))
	require.NoError(t, k.SetPool(ctx, 1, p))

	require.NoError(t, k.RebaseVolumeIndex(ctx))
	idx, err := k.VolumeIndex.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, below, idx)
	after, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, p.VolumeWeight, after.VolumeWeight)
}

// Without the rebase the index would compound past 2^256 and panic. Run a
// decade of daily steps with a rebase each block, as EndBlock would, and check
// it stays bounded.
func TestVolumeIndexStaysBoundedWithRebase(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	require.NoError(t, k.AdvanceVolumeIndex(ctx)) // anchor the day
	ceiling := math.NewIntWithDecimal(1, 18).MulRaw(types.VolumeIndexRebaseAt).MulRaw(2)
	for day := 1; day <= 3650; day++ {
		ctx = ctx.WithBlockTime(ctx.BlockTime().AddDate(0, 0, 1))
		require.NoError(t, k.AdvanceVolumeIndex(ctx))
		require.NoError(t, k.MaybeRebaseVolumeIndex(ctx))
		idx, err := k.VolumeIndex.Get(ctx)
		require.NoError(t, err)
		require.True(t, idx.LT(ceiling), "day %d: index %s", day, idx)
	}
}
