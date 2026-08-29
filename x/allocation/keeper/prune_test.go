package keeper

import (
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

const graceSeconds = types.OptionIdleGrace

func (e *testEnv) advance(d time.Duration) {
	e.ctx = e.ctx.WithBlockTime(e.ctx.BlockTime().Add(d))
}

func (e *testEnv) hasOption(id uint64) bool {
	has, err := e.k.Options.Has(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	return err == nil && has
}

// An option nobody ever voted for is a row the chain would otherwise store
// forever for a one-time fee. After the grace period it goes.
func TestDeadOptionIsRemovedAfterTheGracePeriod(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id := addDeadOption(t, e, "nobody voted for this")

	// One second short of the grace period: still there.
	e.advance(time.Duration(graceSeconds-1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.True(t, e.hasOption(id), "removed before its grace period was up")

	e.advance(time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.False(t, e.hasOption(id))
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// Weight is what keeps an option alive, and the clock starts when it dies, not
// when it was created — otherwise an option voted for on day 29 would be swept
// away on day 30 with a live voter still pointing at it.
func TestVotedOptionSurvivesAndItsClockRestarts(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	id := addDeadOption(t, e, "someone votes for this")

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	vote := func(optionID uint64) {
		_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
			Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
			Percentages: []types.AllocationWeight{{OptionId: optionID, Percent: 100}},
		})
		require.NoError(t, err)
	}
	vote(id)

	// Well past the grace period, but it is carrying weight.
	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.True(t, e.hasOption(id), "an option carrying weight must never be removed")

	// The vote moves away. The clock starts here, not at creation, so it lives
	// through a block that is already long past its creation plus the grace.
	vote(types.LPRewardsOptionID)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.True(t, e.hasOption(id), "the clock restarts when it dies, not when it was made")

	// A claim does not buy it another grace period either — otherwise claiming
	// dust once a month would keep a dead row alive forever.
	_, err := ms.ClaimAllocation(e.ctx, &types.MsgClaimAllocation{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS, OptionId: id,
	})
	require.NoError(t, err)

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.False(t, e.hasOption(id))
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// Unclaimed rewards do not make a row immortal. Thirty days with no voter and no
// claim, and the balance goes with the option — nothing is burned, because an
// option's accrued ERTH is only minted when it is claimed.
func TestUnclaimedRewardsAreForfeitedWithTheOption(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	id := addDeadOption(t, e, "earned something, never claimed")

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.NoError(t, err)

	// Let it earn, then take the vote away without ever claiming.
	e.advance(time.Hour)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: types.LPRewardsOptionID, Percent: 100}},
	})
	require.NoError(t, err)

	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.True(t, opt.Accumulated.IsPositive(), "the option should be owed something to forfeit")

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.False(t, e.hasOption(id), "an unclaimed balance must not keep a dead row alive")

	// Reported, so anyone watching can see what was given up.
	var forfeited string
	for _, ev := range e.ctx.EventManager().Events() {
		if ev.Type != "prune_allocation_option" {
			continue
		}
		for _, a := range ev.Attributes {
			if a.Key == "forfeited" {
				forfeited = a.Value
			}
		}
	}
	require.Equal(t, opt.Accumulated.String(), forfeited)
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// INTEGRATED options are governance's and are resolved by a protocol handler
// every block. One sitting at zero — the registration-reward pool between
// registrations, the emergency fund before anyone votes for it — is idle on
// purpose and must never be swept away.
func TestIntegratedOptionsAreNeverRemoved(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	e.advance(time.Duration(graceSeconds*12) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))

	require.True(t, e.hasOption(types.LPRewardsOptionID))
	require.True(t, e.hasOption(types.CommunityPoolOptionID))
	has, err := e.k.Options.Has(e.ctx, optionKey(types.STREAM_ID_CARETAKER, types.RegistrationRewardOptionID))
	require.NoError(t, err)
	require.True(t, has)
}

// Options added in a burst come due in a burst. The sweep takes its budget and
// leaves the rest at the front of the queue rather than doing all of it at once.
func TestPruneSweepIsCapped(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	const added = types.OptionPruneSweepLimit + 5
	ids := make([]uint64, 0, added)
	for i := 0; i < added; i++ {
		ids = append(ids, addDeadOption(t, e, fmt.Sprintf("dead %d", i)))
	}

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))

	left := 0
	for _, id := range ids {
		if e.hasOption(id) {
			left++
		}
	}
	require.Equal(t, added-types.OptionPruneSweepLimit, left,
		"one block removed %d options, over the cap of %d", added-left, types.OptionPruneSweepLimit)

	// The remainder is not stranded.
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	for _, id := range ids {
		require.False(t, e.hasOption(id))
	}
}

// The safety claim the whole design rests on: a removed option carried no
// weight, so no live voter was pointing at it and neither aggregate moves.
//
// A stored split can still name a removed option, by one route: it was voted for,
// and then a governance reset zeroed every option in the stream and bumped the
// epoch, leaving the voter's record behind as stale. Retiring that vote later
// walks the split and has to tolerate the gap. (There is no other route — a vote
// naming an option at zero percent is rejected outright, so any option a live
// split names carries weight and is never prunable.)
func TestRemovingAnOptionCannotBreakAVoterOrTheInvariant(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	authority, _ := e.k.addressCodec.BytesToString(e.k.GetAuthority())
	id := addDeadOption(t, e, "voted for, then reset away")

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.NoError(t, err)

	// A zero-percent entry is refused, so this is the only way a split can end up
	// naming an option that later becomes prunable.
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{
			{OptionId: types.LPRewardsOptionID, Percent: 100},
			{OptionId: id, Percent: 0},
		},
	})
	require.ErrorIs(t, err, types.ErrBadPercentages)

	// The reset zeroes every option and bumps the epoch. No time has passed, so
	// the option accrued nothing and is now dead.
	_, err = ms.ResetAllocations(e.ctx, &types.MsgResetAllocations{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS,
	})
	require.NoError(t, err)

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.False(t, e.hasOption(id))

	total, err := e.k.getTotalWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "the reset left %s behind", total)
	require.NoError(t, e.k.AssertInvariants(e.ctx))

	// The voter's stale split still names the option that is now gone.
	addrBz, err := e.k.addressCodec.StringToBytes(voter)
	require.NoError(t, err)
	require.NoError(t, e.k.ClearVoter(e.ctx, types.STREAM_ID_GROUNDWORKS, addrBz))
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// The schedule is derived state and is not exported, so an import has to rebuild
// it. Without that, every option that was dead at export would be immortal.
func TestPruneScheduleIsRebuiltOnImport(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id := addDeadOption(t, e, "dead at export")

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))
	require.True(t, fresh.hasOption(id))

	fresh.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, fresh.k.BeginBlocker(fresh.ctx))
	require.False(t, fresh.hasOption(id))
	require.NoError(t, fresh.k.AssertInvariants(fresh.ctx))
}

// The prune halt, asserted against the check that actually halts the chain.
//
// TestUnclaimedRewardsAreForfeitedWithTheOption above covers the same bug now
// that AssertInvariants walks the accrued sum, and that is the more valuable
// half — it means the next setOption bypass is caught by a test nobody had to
// remember to write. This one exists because the two assertions are not the
// same function and the gap between them is exactly where this shipped:
// AssertInvariants is what the suite calls, AssertHotInvariants is what the
// EndBlocker calls, and for a while only the second one checked solvency.
//
// The sequencing is the point. SweepPrunableOptions runs in BeginBlock and
// AssertHotInvariants in EndBlock, so a prune that unbalances the ledger halts
// the chain in the same block that performed it — no delay, no operator warning,
// and permanent, because the burn already happened and replaying the block
// reproduces it.
func TestPruningAnOptionWithABalanceKeepsTheModuleSolvent(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	id := addDeadOption(t, e, "accrues, then is abandoned")

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.NoError(t, err)

	// Accrue a real balance, then withdraw the vote so the option is prunable
	// while still holding coins the module minted for it.
	e.advance(time.Hour)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: types.LPRewardsOptionID, Percent: 100}},
	})
	require.NoError(t, err)

	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.True(t, opt.Accumulated.IsPositive(), "nothing is being tested unless there is a balance to forfeit")

	before, err := e.k.GetSummedAccrued(e.ctx)
	require.NoError(t, err)

	// The EndBlocker's own check must pass before the sweep, or the test proves
	// nothing about what the sweep did.
	require.NoError(t, e.k.AssertHotInvariants(e.ctx), "precondition: solvent before the prune")

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.False(t, e.hasOption(id), "the option should have been swept")

	// The ledger moved by exactly what was burned.
	after, err := e.k.GetSummedAccrued(e.ctx)
	require.NoError(t, err)
	require.Equal(t, before.Sub(opt.Accumulated).String(), after.String(),
		"summed_accrued must fall by the forfeited balance the burn removed")

	// The check the EndBlocker runs, in the block the sweep ran in.
	require.NoError(t, e.k.AssertHotInvariants(e.ctx),
		"burning a forfeited balance without decrementing summed_accrued halts the chain here")

	// And the exhaustive walk agrees, so neither figure is merely self-consistent.
	require.NoError(t, e.k.AssertInvariants(e.ctx))

	sol, err := e.k.CheckSolvency(e.ctx)
	require.NoError(t, err)
	require.True(t, sol.Short.IsZero(), "module must not be short after a prune")
}
