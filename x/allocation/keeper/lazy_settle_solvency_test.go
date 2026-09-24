package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// A lazily-settled option truncates once over many blocks' deltas, so it
// collects the fractional uerth of every block in between. When AdvanceIndex
// rounded the options' reserve down, those fractions went to residue instead,
// and the first settle after enough blocks left the module short of what its
// options were owed — which AssertHotInvariants, run from EndBlock, reports as a
// chain halt.
//
// The weight 1,000,003 is chosen so the total does not divide
// reward*indexPrecision, which is what produced a fraction every block.
func TestLazySettleStaysSolvent_Groundworks(t *testing.T) {
	e := newTestEnv(t)
	ms := NewMsgServerImpl(e.k)
	staker, stakerStr := e.addr("staker")
	e.staking.bonded[staker.String()] = math.NewInt(1_000_003)
	id := addDeadOption(t, e, "address option")
	vote := &types.MsgSetAllocations{
		Creator:     stakerStr,
		Stream:      types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	}

	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	_, err := ms.SetAllocations(e.ctx, vote)
	require.NoError(t, err)

	for i := 1; i <= 50; i++ {
		e.ctx = e.ctx.WithBlockTime(start.Add(time.Duration(i) * 6 * time.Second))
		require.NoError(t, e.k.BeginBlocker(e.ctx))
		require.NoError(t, e.k.SweepResidue(e.ctx))
		require.NoError(t, e.k.AssertHotInvariants(e.ctx))
	}

	// Re-casting the vote settles the option over all 50 blocks at once.
	_, err = ms.SetAllocations(e.ctx, vote)
	require.NoError(t, err)
	require.NoError(t, e.k.SweepResidue(e.ctx))
	require.NoError(t, e.k.AssertHotInvariants(e.ctx))
}

// The caretaker case: ADDRESS options are permissionless there and the total
// is 100 per human, so any human count with a prime factor other than 2 or 5
// leaves a fraction every block. One option is settled every block, the other
// only at the end.
func TestLazySettleStaysSolvent_Caretaker(t *testing.T) {
	e := newTestEnv(t)
	s := types.STREAM_ID_CARETAKER
	w := math.NewInt(types.HumanVoterWeight)
	busy := addCaretakerOption(t, e)
	lazy := addCaretakerOption(t, e)

	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	require.NoError(t, e.k.AdvanceIndex(e.ctx, s))

	var busyVoter, lazyVoter sdk.AccAddress
	for i := 0; i < 7; i++ {
		a, _ := e.addr(string(rune('a' + i)))
		e.humans.add(a)
		opt := busy
		if i == 6 {
			opt, lazyVoter = lazy, a
		} else if i == 0 {
			busyVoter = a
		}
		require.NoError(t, e.k.resyncVoter(e.ctx, s, a, []types.AllocationWeight{{OptionId: opt, Percent: 100}}, w))
	}

	sweep := func() {
		r, err := e.k.GetResidue(e.ctx)
		require.NoError(t, err)
		if r.IsPositive() {
			e.bank.debit(sdk.NewCoins(sdk.NewCoin("uerth", r)))
			require.NoError(t, e.k.Residue.Set(e.ctx, math.ZeroInt()))
		}
	}
	for i := 1; i <= 1000; i++ {
		e.ctx = e.ctx.WithBlockTime(start.Add(time.Duration(i) * 6 * time.Second))
		require.NoError(t, e.k.AdvanceIndex(e.ctx, s))
		require.NoError(t, e.k.resyncVoter(e.ctx, s, busyVoter, []types.AllocationWeight{{OptionId: busy, Percent: 100}}, w))
		sweep()
		rep, err := e.k.CheckSolvency(e.ctx)
		require.NoError(t, err)
		require.False(t, rep.Broken(), "block %d: short %s", i, rep.Short)
	}

	require.NoError(t, e.k.resyncVoter(e.ctx, s, lazyVoter, []types.AllocationWeight{{OptionId: lazy, Percent: 100}}, w))
	rep, err := e.k.CheckSolvency(e.ctx)
	require.NoError(t, err)
	require.False(t, rep.Broken(), "after the lazy settle: short %s", rep.Short)
	// The reserve is rounded up by under one uerth per block, so the surplus is
	// bounded by the block count plus one per settle, never more.
	require.True(t, rep.Surplus.LTE(math.NewInt(1000+1000+2)), "surplus %s", rep.Surplus)
}

func TestCeilQuo(t *testing.T) {
	for _, c := range []struct{ n, d, want int64 }{
		{0, 5, 0}, {1, 5, 1}, {5, 5, 1}, {6, 5, 2}, {10, 5, 2},
	} {
		require.Equal(t, c.want, ceilQuo(math.NewInt(c.n), math.NewInt(c.d)).Int64(), "%d/%d", c.n, c.d)
	}
}
