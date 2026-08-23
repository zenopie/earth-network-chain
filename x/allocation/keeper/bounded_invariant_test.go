package keeper

import (
	"fmt"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// seedAddressOptions adds n permissionless ADDRESS options to the capital stream, which
// is what an outsider does for a fee and what the per-block check used to walk.
func seedAddressOptions(t *testing.T, e *testEnv, n int) {
	t.Helper()
	_, recipient := e.addr("recipient")
	for i := 0; i < n; i++ {
		_, err := e.k.appendOption(e.ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
			Description: fmt.Sprintf("option %d", i),
			Kind:        types.ALLOCATION_KIND_ADDRESS,
			Recipient:   recipient,
		})
		require.NoError(t, err)
	}
}

// The point of the change. Adding an ADDRESS option is permissionless, so a
// per-block check that walks the options is a one-time fee buying unmetered work
// on every block afterwards — the same trade x/dex had to undo for pools.
//
// Measured in store reads rather than in time, which is the thing that actually
// scales and the thing a benchmark on a fast laptop would hide.
func TestInvariantCostIsFlatInOptionCount(t *testing.T) {
	cost := func(options int) uint64 {
		e := newTestEnv(t)
		require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
		seedAddressOptions(t, e, options)

		_, voter := e.addr("voter")
		e.staking.bonded[voter] = math.NewInt(1_000_000)
		ms := NewMsgServerImpl(e.k)
		_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
			Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
			Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
		})
		require.NoError(t, err)

		gm := storetypes.NewInfiniteGasMeter()
		require.NoError(t, e.k.AssertHotInvariants(e.ctx.WithGasMeter(gm)))
		return gm.GasConsumed()
	}

	small := cost(5)
	large := cost(500)

	// Not "grows slowly" — flat. The check reads two numbers per stream and
	// nothing else, so a hundred times the options is the same work exactly.
	require.Equal(t, small, large,
		"per-block invariant cost tracks option count: %d gas at 5 options, %d at 500", small, large)
	t.Logf("per-block invariant cost: %d gas, independent of option count", small)
}

// Making it cheap must not make it blind. This is the drift the check exists
// for: the denominator moves without the parts, so from here the stream divides
// its emission by one figure while the options collect against another.
func TestBoundedCheckStillCatchesDrift(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)
	require.NoError(t, e.k.AssertHotInvariants(e.ctx))

	total, err := e.k.getTotalWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.NoError(t, e.k.TotalWeight.Set(e.ctx, key(types.STREAM_ID_GROUNDWORKS), total.AddRaw(1)))

	err = e.k.AssertHotInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "its options allocate")
}

// The clamp is the case that decides whether an O(1) check is worth having at
// all. resyncVoter floors a negative total at zero while the options it wrote
// keep their balances; a single aggregate incremented alongside itself would
// move with the clamp and see nothing. The running sum is maintained from what
// each option actually took and is never clamped, so the two part ways here.
func TestBoundedCheckStillCatchesTheClampedCase(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)

	// The state the clamp leaves behind: total zeroed, options still allocated.
	require.NoError(t, e.k.TotalWeight.Set(e.ctx, key(types.STREAM_ID_GROUNDWORKS), math.ZeroInt()))

	err = e.k.AssertHotInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "total_weight is 0")
}

// What the per-block check gives up, stated as a test rather than left in a
// comment. An option written around setOption, with TotalWeight left alone,
// leaves both aggregates ignorant of it: they still agree with each other, so
// the bounded check passes. Only the walk sees the option itself — which is why
// the walk still exists and the tests still run it after every operation.
func TestExhaustiveCheckCatchesAWriteThatBypassedSetOption(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	require.NoError(t, e.k.AssertInvariants(e.ctx))

	stream := types.STREAM_ID_GROUNDWORKS
	opt, err := e.k.Options.Get(e.ctx, optionKey(stream, 1))
	require.NoError(t, err)
	opt.AmountAllocated = math.NewInt(777)
	require.NoError(t, e.k.Options.Set(e.ctx, optionKey(stream, 1), opt))

	require.NoError(t, e.k.AssertHotInvariants(e.ctx),
		"both aggregates missed the write, so they agree and the bounded check is blind")

	err = e.k.AssertInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "its options allocate 777")
}

// And what it is for once the chain has halted. The bounded check can only say
// that two numbers disagree; the walk is what says which of them is wrong, so an
// operator reading a halted node learns whether the total drifted or an option
// was written around setOption.
func TestExhaustiveCheckNamesAStaleRunningSum(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	stream := types.STREAM_ID_GROUNDWORKS
	opt, err := e.k.Options.Get(e.ctx, optionKey(stream, 1))
	require.NoError(t, err)
	opt.AmountAllocated = math.NewInt(777)
	require.NoError(t, e.k.Options.Set(e.ctx, optionKey(stream, 1), opt))
	// The bypassing path kept TotalWeight correct and only forgot the sum.
	require.NoError(t, e.k.TotalWeight.Set(e.ctx, key(stream), math.NewInt(777)))

	require.ErrorIs(t, e.k.AssertHotInvariants(e.ctx), types.ErrInvariantBroken)

	err = e.k.AssertInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "without going through setOption")
}

// A reset zeroes every option's weight, so the running sum has to come back to
// zero with it — otherwise the next vote is measured against a stream that
// still believes it carries the weight it just retired.
func TestRunningSumFollowsAReset(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	authority, _ := e.k.addressCodec.BytesToString(e.k.GetAuthority())

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)

	sum, err := e.k.getSummedWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000), sum)

	_, err = ms.ResetAllocations(e.ctx, &types.MsgResetAllocations{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS,
	})
	require.NoError(t, err)

	sum, err = e.k.getSummedWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, sum.IsZero(), "the reset left %s behind", sum)
	require.NoError(t, e.k.AssertHotInvariants(e.ctx))
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// An import restores the options verbatim, so the running sum has to be rebuilt
// from them. If it were left at zero the chain would halt on its own invariant
// at height 1 — with a genesis file that validate-genesis had just accepted.
func TestRunningSumIsRebuiltOnImport(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(4_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	sum, err := fresh.k.getSummedWeight(fresh.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(4_000_000), sum)
	require.NoError(t, fresh.k.AssertHotInvariants(fresh.ctx))
	require.NoError(t, fresh.k.AssertInvariants(fresh.ctx))
}
