package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
)

// Audit round 2, CD-1: a lapse backlog (more lapse seconds due at once than
// one block used to walk: after a long halt) left leases due after
// x/allocation's BeginBlocker. A tx that read a vote or voter, then
// settled the stream (which retired those leases), then wrote what it had
// read, undid or doubled the lapse. Leases now retire only in the
// BeginBlock sweep, which drains every due one; these reproduce each effect
// with more than 1000 due lapse seconds.

// backlogSeconds is how many distinct lapse seconds the backlog holds: more
// than one settle used to walk (1000). Before the fix, the late block's
// BeginBlock settle and one EndBlock settle each walked 1000 of them, so
// 2100 leaves the target lease due at tx time, and 1100 has it retired by
// the block's own EndBlock settle (a re-file of the validator's voter).
const backlogSeconds = 2100

// lapseBacklog queues n account leases at the n seconds before end (fillers:
// no voter behind them, so each retires nothing but costs a lapse second).
func (e *stakeEnv) lapseBacklog(end int64, n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		t := end - int64(n) + int64(i)
		require.NoError(e.t, e.app.AllocationKeeper.VoterLapses.Set(e.ctx(),
			collections.Join3(t, uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(fmt.Sprintf("backlog-filler-%05d", i)))))
	}
}

// Effects 1 and 2 (a lapsed stake vote's weight resurrected, a double
// subtraction) concerned the stake votes' leases, retired in v1.3.0: stake
// votes end with their vintage (vintages.go), never at a lease.

// Effect 3: an operator's vote written back with no lease. A delegation
// change read the voter, settled (which retired the lapsed vote), then
// re-filed the split it had read, with expires_at 0: counting forever.
func TestGroundworksLeaseBacklogOperator(t *testing.T) {
	g := initGwWeightEnv(t)
	setGwLease(t, g.stakeEnv)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	vk := collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op))
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), vk)
	require.NoError(t, err)
	exp := v.ExpiresAt

	g.atUnix(exp - backlogSeconds - 100)
	g.lapseBacklog(exp, backlogSeconds)
	g.atUnix(exp + 10)

	g.undelegate(val, math.NewInt(100*ssErth))
	v, err = g.app.AllocationKeeper.Voters.Get(g.ctx(), vk)
	if err == nil {
		require.NotZero(t, v.ExpiresAt, "a Groundworks split is never filed without a lease")
	}
	require.True(t, errors.Is(err, collections.ErrNotFound), "the lapsed vote stays retired")
	require.Equal(t, base, g.allocated())
	require.NoError(t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
}
