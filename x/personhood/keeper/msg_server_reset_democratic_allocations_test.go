package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
)

func TestResetDemocraticAllocations(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_000_000, 0).UTC())

	require.NoError(t, f.keeper.Params.Set(sdkCtx, types.DefaultParams()))
	authority, err := f.addressCodec.BytesToString(f.keeper.GetAuthority())
	require.NoError(t, err)

	addr := sdk.AccAddress("active-human________")
	seedVoter(t, f, sdkCtx, []byte("nullifier-1"), addr, 1_000_000)

	// Let the stream run so the option has a real accrued balance to protect.
	require.NoError(t, f.keeper.DemLastUpkeep.Set(sdkCtx, sdkCtx.BlockTime().UnixNano()))
	laterCtx := sdkCtx.WithBlockTime(sdkCtx.BlockTime().Add(60 * time.Second))
	require.NoError(t, f.keeper.BeginBlocker(laterCtx))

	optBefore, err := f.keeper.DemOptions.Get(laterCtx, 1)
	require.NoError(t, err)
	require.True(t, optBefore.AmountAllocated.IsPositive(), "voter weight should be on the option")

	resp, err := ms.ResetDemocraticAllocations(laterCtx, &types.MsgResetDemocraticAllocations{Authority: authority})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Epoch)

	// Weights are cleared: nothing is directed until people vote again.
	total, err := f.keeper.DemTotalWeight.Get(laterCtx)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "total weight should be zero after a reset")

	optAfter, err := f.keeper.DemOptions.Get(laterCtx, 1)
	require.NoError(t, err)
	require.True(t, optAfter.AmountAllocated.IsZero(), "option weight should be cleared")

	// But the ERTH it already earned is not confiscated.
	require.True(t, optAfter.Accumulated.GTE(optBefore.Accumulated),
		"a reset must not take back ERTH the option already accrued")

	// Registrations are untouched — this is a re-vote, not a re-registration.
	_, err = f.keeper.Registrations.Get(laterCtx, []byte("nullifier-1"))
	require.NoError(t, err, "reset must not touch registrations")
	count, err := f.keeper.RegCount.Get(laterCtx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), count)
}

// TestResetThenRevoteDoesNotGoNegative is the correctness property the epoch
// exists for. The reset zeroes the aggregates without rewriting voter rows, so a
// voter whose stale record survives must not have its old weight subtracted a
// second time when they vote again.
func TestResetThenRevoteDoesNotGoNegative(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_000_000, 0).UTC())

	require.NoError(t, f.keeper.Params.Set(sdkCtx, types.DefaultParams()))
	authority, err := f.addressCodec.BytesToString(f.keeper.GetAuthority())
	require.NoError(t, err)

	addr := sdk.AccAddress("returning-human_____")
	seedVoter(t, f, sdkCtx, []byte("nullifier-1"), addr, 1_000_000)
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)

	_, err = ms.ResetDemocraticAllocations(sdkCtx, &types.MsgResetDemocraticAllocations{Authority: authority})
	require.NoError(t, err)

	// The stale vote is still in the store, from the pre-reset epoch.
	stale, err := f.keeper.DemVoters.Get(sdkCtx, addr.Bytes())
	require.NoError(t, err)
	require.Zero(t, stale.Epoch)

	// Voting again must add weight from zero, not subtract the cleared weight.
	_, err = ms.SetDemocraticAllocations(sdkCtx, &types.MsgSetDemocraticAllocations{
		Creator:     addrStr,
		Percentages: []types.DemocraticWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)

	opt, err := f.keeper.DemOptions.Get(sdkCtx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(types.VoterWeight), opt.AmountAllocated.Int64(),
		"re-vote should restore exactly one vote of weight, got %s", opt.AmountAllocated)
	require.False(t, opt.AmountAllocated.IsNegative())

	total, err := f.keeper.DemTotalWeight.Get(sdkCtx)
	require.NoError(t, err)
	require.Equal(t, int64(types.VoterWeight), total.Int64())

	// The new record carries the current epoch, so the next change unwinds it.
	fresh, err := f.keeper.DemVoters.Get(sdkCtx, addr.Bytes())
	require.NoError(t, err)
	require.Equal(t, uint64(1), fresh.Epoch)

	_, err = ms.SetDemocraticAllocations(sdkCtx, &types.MsgSetDemocraticAllocations{
		Creator:     addrStr,
		Percentages: nil,
	})
	require.NoError(t, err)
	total, err = f.keeper.DemTotalWeight.Get(sdkCtx)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "clearing a current-epoch vote should unwind it exactly")
}

func TestResetDemocraticAllocationsRequiresAuthority(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)
	require.NoError(t, f.keeper.Params.Set(f.ctx, types.DefaultParams()))

	notAuthority, err := f.addressCodec.BytesToString(sdk.AccAddress("random-account______"))
	require.NoError(t, err)

	_, err = ms.ResetDemocraticAllocations(f.ctx, &types.MsgResetDemocraticAllocations{Authority: notAuthority})
	require.Error(t, err, "only the module authority may reset the slate")

	epoch, err := f.keeper.DemEpoch.Get(f.ctx)
	if err != nil {
		require.ErrorIs(t, err, collections.ErrNotFound)
	} else {
		require.Zero(t, epoch, "a rejected reset must not bump the epoch")
	}
}
