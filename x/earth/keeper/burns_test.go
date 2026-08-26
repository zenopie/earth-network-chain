package keeper_test

import (
	"testing"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/earth/keeper"
	"github.com/earth-network/earth/x/earth/types"
)

// TestRecordBurnAccumulates: the counter is a running total, not a last-write.
// This is the whole point of keeping it — a burn that overwrote the previous one
// would report the most recent block rather than the chain's history.
func TestRecordBurnAccumulates(t *testing.T) {
	f := initFixture(t)

	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceGasFees, sdk.NewCoins(sdk.NewInt64Coin("uerth", 100))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceGasFees, sdk.NewCoins(sdk.NewInt64Coin("uerth", 250))))

	total, err := f.keeper.TotalBurned(f.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 350)), total)
}

// TestRecordBurnSeparatesSourcesAndDenoms: the key is (source, denom), so two
// mechanisms burning the same denom stay distinguishable and one mechanism
// burning two denoms — which the pol retirement does — keeps them apart.
func TestRecordBurnSeparatesSourcesAndDenoms(t *testing.T) {
	f := initFixture(t)

	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceGasFees, sdk.NewCoins(sdk.NewInt64Coin("uerth", 100))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceSwapFee, sdk.NewCoins(sdk.NewInt64Coin("uerth", 40))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourcePolRetire, sdk.NewCoins(
		sdk.NewInt64Coin("uerth", 7),
		sdk.NewInt64Coin("uusdc", 3),
	)))

	bySource, err := f.keeper.BurnedBySource(f.ctx)
	require.NoError(t, err)
	require.Equal(t, []types.BurnTotal{
		{Source: types.SourceGasFees, Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 100))},
		{Source: types.SourcePolRetire, Amount: sdk.NewCoins(
			sdk.NewInt64Coin("uerth", 7), sdk.NewInt64Coin("uusdc", 3))},
		{Source: types.SourceSwapFee, Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 40))},
	}, bySource, "sorted by source, with each source's denoms kept apart")

	total, err := f.keeper.TotalBurned(f.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoins(
		sdk.NewInt64Coin("uerth", 147),
		sdk.NewInt64Coin("uusdc", 3),
	), total, "the rollup sums across sources, per denom")
}

// TestRecordBurnIgnoresNonPositive: several callers hand over a coin set that is
// legitimately empty on the block in question, and a zero must not create a key
// — an absent source and a source that has burned nothing read differently.
func TestRecordBurnIgnoresNonPositive(t *testing.T) {
	f := initFixture(t)

	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceSwapFee, sdk.NewCoins()))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceSwapFee, sdk.Coins{sdk.Coin{Denom: "uerth", Amount: math.ZeroInt()}}))

	bySource, err := f.keeper.BurnedBySource(f.ctx)
	require.NoError(t, err)
	require.Empty(t, bySource)
}

// TestBurnsSurviveExportImport is the reason the counters are in genesis at all.
// They cannot be reconstructed from anything the chain keeps: x/bank records the
// supply that remains, never what was taken out of it. An export that dropped
// them would report a chain that had never burned, and nothing observed later
// could correct it.
func TestBurnsSurviveExportImport(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceAnmlBuyback, sdk.NewCoins(sdk.NewInt64Coin("uanml", 900))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceGasFees, sdk.NewCoins(sdk.NewInt64Coin("uerth", 12))))
	require.NoError(t, f.keeper.LastMintTime.Set(f.ctx, 1_700_000_000_000_000_000))

	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	fresh := initFixture(t)
	require.NoError(t, fresh.keeper.InitGenesis(fresh.ctx, *exported))

	total, err := fresh.keeper.TotalBurned(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoins(
		sdk.NewInt64Coin("uanml", 900),
		sdk.NewInt64Coin("uerth", 12),
	), total)

	// The emission clock rides along for the same reason: a zero here makes the
	// next block mint nothing and restart, silently skipping one block's issuance.
	last, err := fresh.keeper.GetLastMintTime(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000_000_000), last)
}

// TestGenesisRejectsDuplicateSource: a repeated source would import as one entry
// silently overwriting the other, and the total afterwards would be short by
// whichever lost.
func TestGenesisRejectsDuplicateSource(t *testing.T) {
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		Burned: []types.BurnTotal{
			{Source: types.SourceGasFees, Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))},
			{Source: types.SourceGasFees, Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 2))},
		},
	}
	require.ErrorContains(t, gs.Validate(), "duplicate burn source")
}

// TestBurnsQueryReportsSourcesAndTotal exercises what the explorer actually
// calls: the per-source breakdown and the rolled-up total in one response, so
// every client agrees on the headline figure instead of each summing its own.
func TestBurnsQueryReportsSourcesAndTotal(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceSwapFee, sdk.NewCoins(sdk.NewInt64Coin("uerth", 25))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceAnmlBuyback, sdk.NewCoins(sdk.NewInt64Coin("uanml", 900))))
	require.NoError(t, f.keeper.RecordBurn(f.ctx, types.SourceGasFees, sdk.NewCoins(sdk.NewInt64Coin("uerth", 5))))

	resp, err := keeper.NewQueryServerImpl(f.keeper).Burns(f.ctx, &types.QueryBurnsRequest{})
	require.NoError(t, err)

	require.Len(t, resp.BySource, 3)
	require.Equal(t, types.SourceAnmlBuyback, resp.BySource[0].Source, "sorted by source name")
	require.Equal(t, sdk.NewCoins(
		sdk.NewInt64Coin("uanml", 900),
		sdk.NewInt64Coin("uerth", 30),
	), resp.Total)
}

// TestBurnsQueryOnAFreshChain: nothing has burned yet, and the query says so
// rather than failing. An explorer rendering a new chain has to get zero, not an
// error page.
func TestBurnsQueryOnAFreshChain(t *testing.T) {
	f := initFixture(t)

	resp, err := keeper.NewQueryServerImpl(f.keeper).Burns(f.ctx, &types.QueryBurnsRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.BySource)
	require.True(t, resp.Total.IsZero())
}
