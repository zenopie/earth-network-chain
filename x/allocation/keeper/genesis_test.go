package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// A fresh chain still seeds both streams and their option #1 — the empty-stream
// list is what tells InitGenesis which case it is in.
func TestGenesisSeedsWhenNoStreamsAreCarried(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	for _, stream := range types.Streams {
		opt, err := e.k.Options.Get(e.ctx, optionKey(stream, 1))
		require.NoError(t, err, "stream %s should have its option #1", stream)
		require.Equal(t, types.ALLOCATION_KIND_INTEGRATED, opt.Kind)
	}
}

// The test that would have caught the export dropping every option and vote.
//
// It builds real state through the message path — an option added, weight
// allocated to it, the index advanced so the option has accrued something —
// exports it, imports into a fresh keeper, and compares. Doing it through the
// keeper rather than by writing structs is deliberate: a hand-built fixture can
// only lose what the author remembered to put in it.
func TestGenesisRoundTripsPopulatedStreams(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	_, voterStr := e.addr("voter-one")
	_, recipientStr := e.addr("recipient")

	// A permissionlessly-added option, which cost its author a burned fee.
	id, err := e.k.appendOption(e.ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
		Description: "A public good",
		Kind:        types.ALLOCATION_KIND_ADDRESS,
		Recipient:   recipientStr,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), id, "options #1 and #2 are the seeded ones")

	// Give the stream some weight pointed at it, then let the index advance so
	// the option is owed something that has not been claimed.
	e.staking.bonded[voterStr] = math.NewInt(1_000_000)
	ms := NewMsgServerImpl(e.k)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voterStr, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.NoError(t, err)
	e.ctx = e.ctx.WithBlockTime(e.ctx.BlockTime().Add(time.Hour))
	require.NoError(t, e.k.AdvanceIndex(e.ctx, types.STREAM_ID_GROUNDWORKS))

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	// Everything that matters actually left the module.
	gw := streamState(t, exported, types.STREAM_ID_GROUNDWORKS)
	require.Len(t, gw.Options, 3, "the two seeded options and the added one")
	require.Len(t, gw.Voters, 1)
	require.True(t, gw.TotalWeight.IsPositive(), "the stream's weight must survive")
	require.True(t, gw.RewardIndex.IsPositive(), "the reward index must survive")
	require.Equal(t, uint64(3), gw.OptionSeq, "the id sequence must not go backwards")

	// And it comes back the same.
	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	reExported, err := fresh.k.ExportGenesis(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, exported.Streams, reExported.Streams)

	// Spot-check through the store rather than only through the exporter, so a
	// symmetric bug in both directions cannot pass.
	opt, err := fresh.k.Options.Get(fresh.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.Equal(t, "A public good", opt.Description)
	require.Equal(t, recipientStr, opt.Recipient)
	require.True(t, opt.AmountAllocated.IsPositive(), "the vote pointed at it must survive")

	idx, err := fresh.k.getRewardIndex(fresh.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.Equal(t, gw.RewardIndex, idx)
}

// Re-seeding on top of an import would hand out option id 1 a second time, and
// every voter who had allocated to the original would silently be pointing at
// something else.
func TestGenesisDoesNotReseedOverAnImport(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	_, recipientStr := e.addr("recipient")
	_, err := e.k.appendOption(e.ctx, types.STREAM_ID_CARETAKER, types.AllocationOption{
		Description: "Added later",
		Kind:        types.ALLOCATION_KIND_ADDRESS,
		Recipient:   recipientStr,
	})
	require.NoError(t, err)

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)

	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	ct := streamState(t, mustExport(t, fresh), types.STREAM_ID_CARETAKER)
	require.Len(t, ct.Options, 2, "import must not add a third option on top of the two carried")

	first, err := fresh.k.Options.Get(fresh.ctx, optionKey(types.STREAM_ID_CARETAKER, 1))
	require.NoError(t, err)
	require.Equal(t, "Registration rewards", first.Description,
		"option #1 must still be the one that was exported, not a freshly seeded duplicate")
}

// Validate refuses the arithmetic the per-block invariant would otherwise halt
// on at height 1, where it reads as a consensus failure instead of a bad file.
func TestGenesisRejectsWeightThatDoesNotSumToItsOptions(t *testing.T) {
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		Streams: []types.StreamState{{
			Stream:      types.STREAM_ID_GROUNDWORKS,
			RewardIndex: math.ZeroInt(),
			TotalWeight: math.NewInt(500), // claims 500...
			Epoch:       0,
			OptionSeq:   1,
			Options: []types.AllocationOption{{
				Id:              1,
				Stream:          types.STREAM_ID_GROUNDWORKS,
				Description:     "seeded",
				Kind:            types.ALLOCATION_KIND_INTEGRATED,
				AmountAllocated: math.NewInt(300), // ...but the parts are 300
				Accumulated:     math.ZeroInt(),
				LastRewardIndex: math.ZeroInt(),
			}},
		}},
	}
	err := gs.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "total_weight is 500 but the options allocate 300")
}

// A vote for an option that is not in the file would be counted into
// total_weight by nothing and claimable by nobody.
func TestGenesisRejectsAVoteForAMissingOption(t *testing.T) {
	_, voterStr := newTestEnv(t).addr("voter-one")
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		Streams: []types.StreamState{{
			Stream:      types.STREAM_ID_GROUNDWORKS,
			RewardIndex: math.ZeroInt(),
			TotalWeight: math.ZeroInt(),
			OptionSeq:   1,
			Voters: []types.VoterEntry{{
				Address: voterStr,
				Voter: types.Voter{
					Percentages: []types.AllocationWeight{{OptionId: 99, Percent: 100}},
					Weight:      math.ZeroInt(),
				},
			}},
		}},
	}
	err := gs.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "option 99, which does not exist")
}

func streamState(t *testing.T, gs *types.GenesisState, id types.StreamId) types.StreamState {
	t.Helper()
	for _, st := range gs.Streams {
		if st.Stream == id {
			return st
		}
	}
	t.Fatalf("stream %s missing from export", id)
	return types.StreamState{}
}

func mustExport(t *testing.T, e *testEnv) *types.GenesisState {
	t.Helper()
	gs, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	return gs
}

// The stream invariant holds through ordinary voting.
func TestStreamWeightMatchesItsOptions(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, a := e.addr("voter-a")
	_, b := e.addr("voter-b")
	e.staking.bonded[a] = math.NewInt(1_000_000)
	e.staking.bonded[b] = math.NewInt(3_000_000)

	for _, v := range []string{a, b} {
		_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
			Creator: v, Stream: types.STREAM_ID_GROUNDWORKS,
			Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
		})
		require.NoError(t, err)
	}

	rep, err := e.k.CheckStreamWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.False(t, rep.Broken(), "declared %s, options sum to %s", rep.Declared, rep.Summed)
	require.True(t, rep.Declared.IsPositive())
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// The drift the invariant exists for: the denominator and the parts diverge.
// AdvanceIndex divides by one and the options collect against the other, so from
// here the stream pays out something other than the emission it was handed —
// invisibly, because nothing is escrowed to come up short.
func TestStreamWeightCatchesDrift(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, a := e.addr("voter-a")
	e.staking.bonded[a] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: a, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)
	require.NoError(t, e.k.AssertInvariants(e.ctx))

	// Move the denominator without moving an option.
	total, err := e.k.getTotalWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.NoError(t, e.k.TotalWeight.Set(e.ctx, key(types.STREAM_ID_GROUNDWORKS), total.AddRaw(1)))

	rep, err := e.k.CheckStreamWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, rep.Broken())

	err = e.k.AssertInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "its options allocate")
}

// The clamp in resyncVoter turns a negative total into zero, which is exactly
// where a drift would otherwise be absorbed and never seen again. With the
// invariant in place the clamp can no longer hide anything.
func TestStreamWeightCatchesTheClampedCase(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, a := e.addr("voter-a")
	e.staking.bonded[a] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: a, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)

	// The state the clamp leaves behind: total zeroed, options still allocated.
	require.NoError(t, e.k.TotalWeight.Set(e.ctx, key(types.STREAM_ID_GROUNDWORKS), math.ZeroInt()))

	err = e.k.AssertInvariants(e.ctx)
	require.ErrorIs(t, err, types.ErrInvariantBroken)
	require.Contains(t, err.Error(), "total_weight is 0")
}

// Changing an existing vote is the path that subtracts, and subtraction is where
// a running aggregate drifts. resyncVoter removes the voter's previous
// contribution and adds the new one; if either half is missed, TotalWeight and
// the options part ways and the emission is misdivided from then on.
func TestStreamWeightSurvivesRevoting(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)

	_, recipientStr := e.addr("recipient")
	second, err := e.k.appendOption(e.ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
		Description: "A second option",
		Kind:        types.ALLOCATION_KIND_ADDRESS,
		Recipient:   recipientStr,
	})
	require.NoError(t, err)

	_, a := e.addr("voter-a")
	e.staking.bonded[a] = math.NewInt(1_000_000)

	// Vote, then move it, then split it, then withdraw it entirely.
	for _, vote := range [][]types.AllocationWeight{
		{{OptionId: 1, Percent: 100}},
		{{OptionId: second, Percent: 100}},
		{{OptionId: 1, Percent: 40}, {OptionId: second, Percent: 60}},
		{{OptionId: 1, Percent: 100}},
	} {
		_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
			Creator: a, Stream: types.STREAM_ID_GROUNDWORKS, Percentages: vote,
		})
		require.NoError(t, err)
		require.NoErrorf(t, e.k.AssertInvariants(e.ctx), "after voting %v", vote)
	}

	// The weight ends where it started: one voter, one stake, fully allocated.
	rep, err := e.k.CheckStreamWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.False(t, rep.Broken())
	require.Equal(t, math.NewInt(1_000_000), rep.Declared,
		"re-voting must not leave weight behind or duplicate it")
}

// The stream's upkeep cursor must not survive an export, for the same reason the
// personhood buyback clock does not: AdvanceIndex advances by elapsed wall-clock
// time, and a chain restarted from an export did not run during the gap. Carrying
// it would advance the index by the whole downtime in the first block.
func TestGenesisDoesNotCarryTheUpkeepClock(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	// Run the streams so the cursor is genuinely set.
	e.ctx = e.ctx.WithBlockTime(e.ctx.BlockTime().Add(time.Hour))
	require.NoError(t, e.k.AdvanceIndex(e.ctx, types.STREAM_ID_GROUNDWORKS))
	live, err := e.k.getLastUpkeep(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.NotZero(t, live, "the cursor should be set on a running chain")

	gs, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	for _, st := range gs.Streams {
		require.Zerof(t, st.LastUpkeep,
			"stream %s exported a wall-clock cursor; a restart would advance its "+
				"index by the whole downtime in one block", st.Stream)
	}
}
