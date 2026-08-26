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

	weights := seedOptions(t, k, ctx, types.STREAM_ID_CARETAKER, types.MaxVoterOptions+1)
	// Give it a valid sum so only the length can be what rejects it.
	weights[0].Percent = 100

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_CARETAKER,
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

	weights := seedOptions(t, k, ctx, types.STREAM_ID_CARETAKER, 3)
	weights[0].Percent = 100 // sums to 100; the other two are padding

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_CARETAKER,
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

	weights := seedOptions(t, k, ctx, types.STREAM_ID_CARETAKER, types.MaxVoterOptions)
	for i := range weights {
		weights[i].Percent = 5 // 20 x 5 = 100
	}

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_CARETAKER,
		Percentages: weights,
	})
	require.NoError(t, err, "a full-width but valid split must still be accepted")
}

// TestVoterSplitRejectsOverflowingShares is the chain-halt guard. Percent is a
// uint64 with no natural ceiling, and the only sum check is over a uint64 that
// wraps. Two shares near 2^63 sum to exactly 100 modulo 2^64 — passing sum==100
// — and then sign-flip to a negative amount when cast to int64 in resyncVoter,
// driving an option's weight negative. TotalWeight clamps that to zero while
// SummedWeight (never clamped) keeps it, so the next block's stream-weight
// invariant breaks and the EndBlocker halts the chain. A single share over 100
// must be refused before it ever reaches the sum.
func TestVoterSplitRejectsOverflowingShares(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(k)

	acc, addr := e.addr("registered-human")
	e.humans.add(acc)

	// Two shares that overflow the uint64 sum back to exactly 100.
	weights := seedOptions(t, k, ctx, types.STREAM_ID_CARETAKER, 2)
	weights[0].Percent = 1 << 63         // 9223372036854775808
	weights[1].Percent = (1 << 63) + 100 // sum wraps to 100 mod 2^64

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_CARETAKER,
		Percentages: weights,
	})
	require.ErrorIs(t, err, types.ErrBadPercentages,
		"a share over 100 must be rejected before it can overflow the sum")

	// The stream's accounting must be untouched: the vote never landed, so the
	// bounded invariant the EndBlocker runs still holds.
	require.NoError(t, k.AssertHotInvariants(ctx))
	require.NoError(t, k.AssertInvariants(ctx))
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
	seedOptions(t, k, ctx, types.STREAM_ID_CARETAKER, 1)

	_, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator:     addr,
		Stream:      types.STREAM_ID_CARETAKER,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.ErrorIs(t, err, types.ErrNoWeight)

	// Clearing a vote is still allowed — someone whose registration lapsed must
	// be able to tidy up after themselves.
	_, err = ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator: addr,
		Stream:  types.STREAM_ID_CARETAKER,
	})
	require.NoError(t, err)
}
