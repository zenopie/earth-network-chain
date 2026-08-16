package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
)

// seedDemOptions writes n empty democratic options so a split can reference them.
func seedDemOptions(t *testing.T, f *fixture, ctx sdk.Context, n int) []types.DemocraticWeight {
	t.Helper()
	weights := make([]types.DemocraticWeight, 0, n)
	for i := 1; i <= n; i++ {
		require.NoError(t, f.keeper.DemOptions.Set(ctx, uint64(i), types.DemocraticOption{
			Id:              uint64(i),
			Kind:            types.DEMOCRATIC_KIND_ADDRESS,
			AmountAllocated: math.ZeroInt(),
			Accumulated:     math.ZeroInt(),
			LastRewardIndex: math.ZeroInt(),
		}))
		weights = append(weights, types.DemocraticWeight{OptionId: uint64(i), Percent: 0})
	}
	return weights
}

// TestDemocraticSplitIsCapped is a denial-of-service guard, not a usability
// rule. removeRegistration unwinds a lapsed voter's split option by option from
// BeginBlock, so an oversized split is work the chain performs with nobody
// paying gas — multiplied by the expiry sweep limit in a single block.
func TestDemocraticSplitIsCapped(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	// Inside the registration validity window seeded below, so the split rules
	// are what decide these cases rather than an expired registration.
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_000_100, 0).UTC())
	require.NoError(t, f.keeper.Params.Set(sdkCtx, types.DefaultParams()))

	addr := sdk.AccAddress("registered-human____")
	seedVoter(t, f, sdkCtx, []byte("nullifier-cap"), addr, 1_000_000)
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)

	weights := seedDemOptions(t, f, sdkCtx, types.MaxVoterOptions+1)
	// Give it a valid sum so only the length can be what rejects it.
	weights[0].Percent = 100

	_, err = ms.SetDemocraticAllocations(sdkCtx, &types.MsgSetDemocraticAllocations{
		Creator:     addrStr,
		Percentages: weights,
	})
	require.ErrorIs(t, err, types.ErrBadPercentages,
		"a split wider than the cap must be rejected")
}

// TestDemocraticSplitRejectsZeroShares closes the other half of the same hole:
// percentages only have to sum to 100, so padding entries at 0%% would slip past
// a length check while still costing a read-modify-write each on every unwind.
func TestDemocraticSplitRejectsZeroShares(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	// Inside the registration validity window seeded below, so the split rules
	// are what decide these cases rather than an expired registration.
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_000_100, 0).UTC())
	require.NoError(t, f.keeper.Params.Set(sdkCtx, types.DefaultParams()))

	addr := sdk.AccAddress("registered-human____")
	seedVoter(t, f, sdkCtx, []byte("nullifier-zero"), addr, 1_000_000)
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)

	weights := seedDemOptions(t, f, sdkCtx, 3)
	weights[0].Percent = 100 // sums to 100; the other two are padding

	_, err = ms.SetDemocraticAllocations(sdkCtx, &types.MsgSetDemocraticAllocations{
		Creator:     addrStr,
		Percentages: weights,
	})
	require.ErrorIs(t, err, types.ErrBadPercentages,
		"zero-share entries direct nothing and must not be storable")
}

// TestDemocraticSplitAtCapIsAccepted keeps the guard from being over-tight: a
// legitimate split right at the limit still works.
func TestDemocraticSplitAtCapIsAccepted(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	// Inside the registration validity window seeded below, so the split rules
	// are what decide these cases rather than an expired registration.
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_000_100, 0).UTC())
	require.NoError(t, f.keeper.Params.Set(sdkCtx, types.DefaultParams()))

	addr := sdk.AccAddress("registered-human____")
	seedVoter(t, f, sdkCtx, []byte("nullifier-ok"), addr, 1_000_000)
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)

	weights := seedDemOptions(t, f, sdkCtx, types.MaxVoterOptions)
	for i := range weights {
		weights[i].Percent = 5 // 20 x 5 = 100
	}

	_, err = ms.SetDemocraticAllocations(sdkCtx, &types.MsgSetDemocraticAllocations{
		Creator:     addrStr,
		Percentages: weights,
	})
	require.NoError(t, err, "a full-width but valid split must still be accepted")
}
