package keeper

import (
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

func countEvents(ctx sdk.Context, typ string) []sdk.Event {
	var out []sdk.Event
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// Audit round 2, CD-1/CD-3: leases retire only in the BeginBlock sweep,
// which drains every due one (here more than 1000 lapse seconds) at its own
// time; any other settle moves the index only, and stops at a due lapse
// rather than share the emission after it.
func TestLeaseSweepDrainsBacklogExactly(t *testing.T) {
	e := newTestEnv(t)
	k := e.k
	gw := types.STREAM_ID_GROUNDWORKS
	const t0 = int64(1_000_000)
	ctx := e.ctx.WithBlockTime(time.Unix(t0, 0))
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	seedOptions(t, k, ctx, gw, 1)
	require.NoError(t, k.OptionSeq.Set(ctx, key(gw), 1))
	require.NoError(t, k.AdvanceIndex(ctx, gw))
	idx0, err := k.getRewardIndex(ctx, gw)
	require.NoError(t, err)

	split := []types.AllocationWeight{{OptionId: 1, Percent: 100}}
	a, _ := e.addr("lapsing-operator")
	b, _ := e.addr("standing-operator")
	require.NoError(t, k.resyncVoterAt(ctx, gw, a, split, math.NewInt(100), t0+5000))
	require.NoError(t, k.resyncVoterAt(ctx, gw, b, split, math.NewInt(300), t0+10_000))
	const fillers = 1500
	for i := 0; i < fillers; i++ {
		require.NoError(t, k.VoterLapses.Set(ctx, collections.Join3(t0+3000+int64(i), uint32(gw), []byte(fmt.Sprintf("filler-%04d", i)))))
	}

	// A settle that is not the sweep, with leases due: it retires nothing,
	// stops at the first due lapse, and says so.
	late := ctx.WithBlockTime(time.Unix(t0+6000, 0)).WithEventManager(sdk.NewEventManager())
	require.NoError(t, k.AdvanceIndex(late, gw))
	_, err = k.Voters.Get(late, voterKey(gw, a))
	require.NoError(t, err, "a tx-time settle never retires a lease")
	last, err := k.getLastUpkeep(late, gw)
	require.NoError(t, err)
	require.Equal(t, (t0+3000)*int64(time.Second), last, "held at the first due lapse")
	require.Len(t, countEvents(late, types.EventTypeLeaseSettleHeld), 1)

	// The sweep drains all of it, each lapse at its own time.
	require.NoError(t, k.SweepLapses(late, gw))
	_, err = k.Voters.Get(late, voterKey(gw, a))
	require.ErrorIs(t, err, collections.ErrNotFound, "a lapsed")
	_, err = k.Voters.Get(late, voterKey(gw, b))
	require.NoError(t, err)
	left, found, err := k.nextLapse(late, k.lapsersOf(gw), t0+6000)
	require.NoError(t, err)
	require.False(t, found, "nothing due after the sweep (next at %d)", left)
	last, err = k.getLastUpkeep(late, gw)
	require.NoError(t, err)
	require.Equal(t, (t0+6000)*int64(time.Second), last)
	ev := countEvents(late, types.EventTypeLeaseBacklogDrained)
	require.Len(t, ev, 1)
	require.Equal(t, fmt.Sprint(fillers+1), ev[0].Attributes[1].Value)

	// Exact: 3000 s held over 400, then one second at a time over 400 up to
	// the last filler, 501 s over 400 to a's lapse, 1000 s over 300.
	prec := math.NewInt(1_000_000_000_000_000_000)
	step := func(sec, total int64) math.Int {
		return math.NewInt(types.EmissionPerSecond).MulRaw(sec).Mul(prec).QuoRaw(total)
	}
	want := idx0.Add(step(3000, 400))
	for i := 1; i < fillers; i++ {
		want = want.Add(step(1, 400))
	}
	want = want.Add(step(501, 400)).Add(step(1000, 300))
	idx, err := k.getRewardIndex(late, gw)
	require.NoError(t, err)
	require.Equal(t, want, idx, "the emission after a's lapse is shared by b alone")

	// Another settle in the block: nothing due, nothing held.
	again := late.WithEventManager(sdk.NewEventManager())
	require.NoError(t, k.AdvanceIndex(again, gw))
	require.Empty(t, countEvents(again, types.EventTypeLeaseSettleHeld))
	require.NoError(t, k.AssertHotInvariants(again))
}

// A failed retirement is retried a day after the block it failed in, never
// within the same sweep, however old the lease.
func TestLapseRetryAt(t *testing.T) {
	ctx := sdk.Context{}.WithBlockTime(time.Unix(500_000, 0))
	require.Equal(t, int64(500_000+LapseRetrySeconds), LapseRetryAt(ctx, 100))
	require.Equal(t, int64(600_000+LapseRetrySeconds), LapseRetryAt(ctx, 600_000))
}
