package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// seedVoter puts one voter's full weight behind option 1 of a stream, the way
// MsgSetAllocations would.
func seedVoter(t *testing.T, k Keeper, ctx sdk.Context, stream types.StreamId, addr sdk.AccAddress, weight int64) {
	t.Helper()
	require.NoError(t, k.Options.Set(ctx, optionKey(stream, 1), types.AllocationOption{
		Id:              1,
		Stream:          stream,
		Kind:            types.ALLOCATION_KIND_ADDRESS,
		AmountAllocated: math.NewInt(weight),
		Accumulated:     math.ZeroInt(),
		LastRewardIndex: math.ZeroInt(),
	}))
	require.NoError(t, k.Voters.Set(ctx, voterKey(stream, addr.Bytes()), types.Voter{
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
		Weight:      math.NewInt(weight),
	}))
	require.NoError(t, k.TotalWeight.Set(ctx, key(stream), math.NewInt(weight)))
}

func TestResetAllocations(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	authority, err := k.addressCodec.BytesToString(k.GetAuthority())
	require.NoError(t, err)

	stream := types.STREAM_ID_GROUNDWORKS
	addr := sdk.AccAddress(authtypes.NewModuleAddress("staker"))
	seedVoter(t, k, ctx, stream, addr, 1_000)

	resp, err := ms.ResetAllocations(ctx, &types.MsgResetAllocations{Authority: authority, Stream: stream})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Epoch)

	total, err := k.getTotalWeight(ctx, stream)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "total weight should be zero after a reset")

	opt, err := k.Options.Get(ctx, optionKey(stream, 1))
	require.NoError(t, err)
	require.True(t, opt.AmountAllocated.IsZero(), "option weight should be cleared")

	// The stale voter row survives but carries the old epoch, so it no longer
	// counts and will not be double-subtracted when the staker votes again.
	stale, err := k.Voters.Get(ctx, voterKey(stream, addr.Bytes()))
	require.NoError(t, err)
	require.Zero(t, stale.Epoch)

	// Re-applying that stale voter at a live weight must add from zero rather
	// than subtract weight the reset already cleared.
	require.NoError(t, k.resyncVoter(ctx, stream, addr.Bytes(),
		[]types.AllocationWeight{{OptionId: 1, Percent: 100}}, math.NewInt(1_000)))

	opt, err = k.Options.Get(ctx, optionKey(stream, 1))
	require.NoError(t, err)
	require.Equal(t, int64(1_000), opt.AmountAllocated.Int64(),
		"re-vote should restore exactly one stake of weight, got %s", opt.AmountAllocated)
	require.False(t, opt.AmountAllocated.IsNegative())
}

// TestResetAllocationsIsPerStream is the trap the merge had to avoid: with one
// module owning both slates, a governance reset of one must leave the other
// standing. Sharing an epoch would silently retire every vote on the chain.
func TestResetAllocationsIsPerStream(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	authority, err := k.addressCodec.BytesToString(k.GetAuthority())
	require.NoError(t, err)

	human := sdk.AccAddress(authtypes.NewModuleAddress("human"))
	staker := sdk.AccAddress(authtypes.NewModuleAddress("staker"))
	seedVoter(t, k, ctx, types.STREAM_ID_CARETAKER, human, types.HumanVoterWeight)
	seedVoter(t, k, ctx, types.STREAM_ID_GROUNDWORKS, staker, 1_000)

	_, err = ms.ResetAllocations(ctx, &types.MsgResetAllocations{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS,
	})
	require.NoError(t, err)

	capitalTotal, err := k.getTotalWeight(ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, capitalTotal.IsZero(), "the reset stream should be cleared")

	humanTotal, err := k.getTotalWeight(ctx, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)
	require.Equal(t, int64(types.HumanVoterWeight), humanTotal.Int64(),
		"resetting one stream must not touch the other's weight")

	humanEpoch, err := k.getEpoch(ctx, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)
	require.Zero(t, humanEpoch, "resetting one stream must not bump the other's epoch")

	opt, err := k.Options.Get(ctx, optionKey(types.STREAM_ID_CARETAKER, 1))
	require.NoError(t, err)
	require.Equal(t, int64(types.HumanVoterWeight), opt.AmountAllocated.Int64(),
		"the untouched stream's options keep their weight")
}

// TestResetAllocationsRejectsCaretaker is the whole point of the stream check:
// stake-weighted governance holds no lever over the caretaker slate, so the
// message is refused before the authority is even considered. A reset takes
// nothing, but a stream with no votes accrues to nothing, so a repeatable reset
// would be a mute button on the fund.
func TestResetAllocationsRejectsCaretaker(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	authority, err := k.addressCodec.BytesToString(k.GetAuthority())
	require.NoError(t, err)

	human := sdk.AccAddress(authtypes.NewModuleAddress("human"))
	seedVoter(t, k, ctx, types.STREAM_ID_CARETAKER, human, types.HumanVoterWeight)

	_, err = ms.ResetAllocations(ctx, &types.MsgResetAllocations{
		Authority: authority, Stream: types.STREAM_ID_CARETAKER,
	})
	require.ErrorIs(t, err, types.ErrStreamNotResettable,
		"governance must not be able to retire the caretaker slate")

	// Nothing moved: the slate, its weight, and its epoch all stand.
	total, err := k.getTotalWeight(ctx, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)
	require.Equal(t, int64(types.HumanVoterWeight), total.Int64(),
		"a rejected reset must leave the caretaker weight standing")

	epoch, err := k.getEpoch(ctx, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)
	require.Zero(t, epoch, "a rejected reset must not bump the epoch")

	voter, err := k.Voters.Get(ctx, voterKey(types.STREAM_ID_CARETAKER, human.Bytes()))
	require.NoError(t, err)
	require.Equal(t, int64(types.HumanVoterWeight), voter.Weight.Int64(),
		"the human's vote must survive the rejected reset")
}

func TestResetAllocationsRequiresAuthority(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	notAuthority, err := k.addressCodec.BytesToString(authtypes.NewModuleAddress("random"))
	require.NoError(t, err)

	_, err = ms.ResetAllocations(ctx, &types.MsgResetAllocations{
		Authority: notAuthority, Stream: types.STREAM_ID_GROUNDWORKS,
	})
	require.Error(t, err, "only the module authority may reset the slate")

	epoch, err := k.getEpoch(ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.Zero(t, epoch, "a rejected reset must not bump the epoch")
}
