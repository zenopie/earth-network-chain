package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/deflation/types"
)

// seedStakeVoter puts one staker's full weight behind option 1, the way
// MsgSetAllocations would.
func seedStakeVoter(t *testing.T, k Keeper, ctx sdk.Context, addr sdk.AccAddress, weight int64) {
	t.Helper()
	require.NoError(t, k.AllocationOptions.Set(ctx, 1, types.AllocationOption{
		Id:              1,
		Kind:            types.ALLOCATION_KIND_ADDRESS,
		AmountAllocated: math.NewInt(weight),
		Accumulated:     math.ZeroInt(),
		LastRewardIndex: math.ZeroInt(),
	}))
	require.NoError(t, k.Voters.Set(ctx, addr.Bytes(), types.Voter{
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
		Weight:      math.NewInt(weight),
	}))
	require.NoError(t, k.TotalWeight.Set(ctx, math.NewInt(weight)))
}

func TestResetAllocations(t *testing.T) {
	k, ctx, _, _ := newKeeperForTest(t)
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	authority, err := k.addressCodec.BytesToString(k.GetAuthority())
	require.NoError(t, err)

	addr := sdk.AccAddress(authtypes.NewModuleAddress("staker"))
	seedStakeVoter(t, k, ctx, addr, 1_000)

	resp, err := ms.ResetAllocations(ctx, &types.MsgResetAllocations{Authority: authority})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Epoch)

	total, err := k.TotalWeight.Get(ctx)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "total weight should be zero after a reset")

	opt, err := k.AllocationOptions.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, opt.AmountAllocated.IsZero(), "option weight should be cleared")

	// The stale voter row survives but carries the old epoch, so it no longer
	// counts and will not be double-subtracted when the staker votes again.
	stale, err := k.Voters.Get(ctx, addr.Bytes())
	require.NoError(t, err)
	require.Zero(t, stale.Epoch)

	// Re-applying that stale voter at a live weight must add from zero rather
	// than subtract weight the reset already cleared.
	require.NoError(t, k.resyncVoter(ctx, addr.Bytes(),
		[]types.AllocationWeight{{OptionId: 1, Percent: 100}}, math.NewInt(1_000)))

	opt, err = k.AllocationOptions.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1_000), opt.AmountAllocated.Int64(),
		"re-vote should restore exactly one stake of weight, got %s", opt.AmountAllocated)
	require.False(t, opt.AmountAllocated.IsNegative())
}

func TestResetAllocationsRequiresAuthority(t *testing.T) {
	k, ctx, _, _ := newKeeperForTest(t)
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	notAuthority, err := k.addressCodec.BytesToString(authtypes.NewModuleAddress("random"))
	require.NoError(t, err)

	_, err = ms.ResetAllocations(ctx, &types.MsgResetAllocations{Authority: notAuthority})
	require.Error(t, err, "only the module authority may reset the slate")

	epoch, err := k.allocationEpoch(ctx)
	require.NoError(t, err)
	require.Zero(t, epoch, "a rejected reset must not bump the epoch")
}
