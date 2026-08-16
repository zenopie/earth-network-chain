package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// seedOptions writes n empty options into a stream so a split can reference them.
func seedOptions(t *testing.T, k Keeper, ctx sdk.Context, stream types.StreamId, n int) []types.AllocationWeight {
	t.Helper()
	weights := make([]types.AllocationWeight, 0, n)
	for i := 1; i <= n; i++ {
		require.NoError(t, k.Options.Set(ctx, optionKey(stream, uint64(i)), types.AllocationOption{
			Id:              uint64(i),
			Stream:          stream,
			Kind:            types.ALLOCATION_KIND_ADDRESS,
			AmountAllocated: math.ZeroInt(),
			Accumulated:     math.ZeroInt(),
			LastRewardIndex: math.ZeroInt(),
		}))
		weights = append(weights, types.AllocationWeight{OptionId: uint64(i), Percent: 0})
	}
	return weights
}

// TestVoterSplitIsCapped is a denial-of-service guard, not a usability rule. A
// stored split is unwound option by option from paths nobody pays gas for — the
// staking hooks in the capital stream, and x/personhood's expiry sweep in the
// human one, where it is multiplied by the sweep limit in a single block.
func TestVoterSplitIsCapped(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	acc, addr := e.addr("registered-human")
	e.humans.add(acc)

	weights := seedOptions(t, k, ctx, types.STREAM_ID_HUMAN, types.MaxVoterOptions+1)
	// Give it a valid sum so only the length can be what rejects it.
	weights[0].Percent = 100

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_HUMAN,
		Percentages: weights,
	})
	require.ErrorIs(t, err, types.ErrBadPercentages,
		"a split wider than the cap must be rejected")
}

// TestVoterSplitRejectsZeroShares closes the other half of the same hole:
// percentages only have to sum to 100, so padding entries at 0% would slip past
// a length check while still costing a read-modify-write each on every unwind.
func TestVoterSplitRejectsZeroShares(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	acc, addr := e.addr("registered-human")
	e.humans.add(acc)

	weights := seedOptions(t, k, ctx, types.STREAM_ID_HUMAN, 3)
	weights[0].Percent = 100 // sums to 100; the other two are padding

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_HUMAN,
		Percentages: weights,
	})
	require.ErrorIs(t, err, types.ErrBadPercentages,
		"zero-share entries direct nothing and must not be storable")
}

// TestVoterSplitAtCapIsAccepted keeps the guard from being over-tight: a
// legitimate split right at the limit still works.
func TestVoterSplitAtCapIsAccepted(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	acc, addr := e.addr("registered-human")
	e.humans.add(acc)

	weights := seedOptions(t, k, ctx, types.STREAM_ID_HUMAN, types.MaxVoterOptions)
	for i := range weights {
		weights[i].Percent = 5 // 20 x 5 = 100
	}

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_HUMAN,
		Percentages: weights,
	})
	require.NoError(t, err, "a full-width but valid split must still be accepted")
}

// TestUnregisteredHumanCannotVote pins the human stream's eligibility rule: the
// weight source returning zero is what stands in for "not a live registration",
// and it has to reject rather than silently record a zero-weight vote.
func TestUnregisteredHumanCannotVote(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	_, addr := e.addr("not-a-human")
	seedOptions(t, k, ctx, types.STREAM_ID_HUMAN, 1)

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_HUMAN,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.ErrorIs(t, err, types.ErrNoWeight)

	// Clearing a vote is still allowed — someone whose registration lapsed must
	// be able to tidy up after themselves.
	_, err = ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator: addr,
		Stream:  types.STREAM_ID_HUMAN,
	})
	require.NoError(t, err)
}
