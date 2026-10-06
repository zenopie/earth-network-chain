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
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// Audit round 2, CD-1: a lapse backlog (more lapse seconds due at once than
// one block used to walk: after a long halt) left leases due after
// x/allocation's BeginBlocker. A tx that read a position or voter, then
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

// Effect 1: a lapsed position's weight resurrected. Updating another
// position at the validator re-filed the voter from totals read before the
// settle that retired the lapsed one: the allocation voter carried the
// lapsed weight (sharing emission after its expiry) while the totals did
// not.
func TestGroundworksLeaseBacklogNoResurrection(t *testing.T) {
	for _, n := range []int{1100, backlogSeconds} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			g := initGwEnv(t)
			setGwLease(t, g.stakeEnv)
			x := g.lockPos(g.v, 1_000*gwE, 1, g.split(100))
			ex := g.storedPosition(x).SplitExpiresAt
			g.next(time.Hour)
			y := g.lockPos(g.v, 2_000*gwE, 2, g.split(0, 100))

			g.atUnix(ex - int64(n) - 100)
			g.lapseBacklog(ex, n)
			g.atUnix(ex + 10) // one late block: the whole backlog and x's lease due
			g.requireEquivalent(g.v, 2)

			g.updatePos(y, 2, g.split(0, 100))
			require.NotContains(t, g.totals(g.v), g.opts[0])
			require.NotContains(t, g.voter(g.v), g.opts[0], "the lapsed weight is not filed again")
			require.True(t, g.allocated(g.opts[0]).IsZero())
			g.requireEquivalent(g.v, 1)
		})
	}
}

// Effect 2: a double subtraction, then a stuck unlock. Unlocking the lapsing
// position took its contribution off the totals, then the settle's lapse
// read the still-stored position and took it off again; the last position
// on that option could then never unlock (groundworks total below zero).
func TestGroundworksLeaseBacklogUnlock(t *testing.T) {
	g := initGwEnv(t)
	setGwLease(t, g.stakeEnv)
	x := g.lockPos(g.v, 1_000*gwE, 1, g.split(100))
	ex := g.storedPosition(x).SplitExpiresAt
	g.next(time.Hour)
	z := g.lockPos(g.v, 2_000*gwE, 2, g.split(100))

	g.atUnix(ex - backlogSeconds - 100)
	g.lapseBacklog(ex, backlogSeconds)
	g.atUnix(ex + 10)

	g.unlockPos(x, 1)
	require.NoError(t, g.tryUnlockPos(z, 2), "the last position on the option unlocks")
	require.Empty(t, g.totals(g.v))
	require.Nil(t, g.voter(g.v))
	require.True(t, g.allocated(g.opts[0]).IsZero())
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))
	require.NoError(t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
}

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

// tryUnlockPos is unlockPos returning the handler's error.
func (g *gwEnv) tryUnlockPos(id uint64, owner int) error {
	st := fakeStake("gw-back-"+fmt.Sprint(id), false)
	st.OwnerTag = ownerTag(owner)
	m := &sstypes.MsgUnlockPosition{PositionId: id, Stake: st}
	_, err := sskeeper.NewMsgServerImpl(g.app.ShieldedStakingKeeper).UnlockPosition(g.fakeAuthorized(m), m)
	return err
}
