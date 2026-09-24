package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

// An export taken mid-stream — rewards paid in but not yet settled into any
// pool, the volume index grown, pools on the staleness clock — must import into
// a state the EndBlock invariants accept, with nothing lost.
//
// This used to halt the first block after import: PendingLpRewards was not
// carried, so the module held ERTH it could not account for.
func TestGenesisRoundTripsLpRewardState(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000_000, 1_000_000_000, 0)
	seedFundedPool(t, k, ctx, bank, 2, 1_000_000_000, 1_000_000_000, 0)
	require.NoError(t, k.AdvanceVolumeIndex(ctx))

	// Grow the index a few days, trading along the way.
	for day := 0; day < 5; day++ {
		ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))
		for _, id := range []uint64{1, 2} {
			p, err := k.Pool.Get(ctx, id)
			require.NoError(t, err)
			require.NoError(t, k.ApplyVolumeForTest(ctx, &p, math.NewInt(int64(1_000_000*id))))
			require.NoError(t, k.SetPool(ctx, id, p))
		}
	}
	// Rewards nobody has settled.
	distributeLP(t, k, ctx, bank, math.NewInt(7_777_777))
	require.NoError(t, k.AssertInvariants(ctx))

	exported, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	wantIdx, err := k.VolumeIndex.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, wantIdx, exported.VolumeIndex)
	require.Len(t, exported.PoolStaleDue, 2)

	// Import into a fresh keeper whose module account holds exactly what the old
	// one did.
	k2, ctx2, bank2 := initRewardFixture(t)
	ctx2 = ctx2.WithBlockTime(ctx.BlockTime())
	bank2.fundModule(bank.modBal...)
	require.NoError(t, k2.InitGenesis(ctx2, *exported))
	require.NoError(t, k2.AssertInvariants(ctx2), "the imported state must pass what EndBlock asserts")

	// What the pools were owed is in their reserves now, not lost.
	var reservesBefore, reservesAfter math.Int = math.ZeroInt(), math.ZeroInt()
	for _, id := range []uint64{1, 2} {
		p, err := k.Pool.Get(ctx, id)
		require.NoError(t, err)
		require.NoError(t, k.SettleForTest(ctx, id, &p))
		reservesBefore = reservesBefore.Add(p.ReserveErth.Amount)
		p2, err := k2.Pool.Get(ctx2, id)
		require.NoError(t, err)
		reservesAfter = reservesAfter.Add(p2.ReserveErth.Amount)
	}
	require.Equal(t, reservesBefore, reservesAfter)

	// The index and the staleness clock survived.
	gotIdx, err := k2.VolumeIndex.Get(ctx2)
	require.NoError(t, err)
	require.Equal(t, wantIdx, gotIdx)
	for _, id := range []uint64{1, 2} {
		want, err := k.PoolStaleDue.Get(ctx, id)
		require.NoError(t, err)
		got, err := k2.PoolStaleDue.Get(ctx2, id)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}
