package keeper

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// A weighted voter puts an absolute weight on each option; replacing or
// clearing it takes off exactly what it put on, it survives an export, and a
// reset retires it like any split.
func TestWeightedVoter(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	gw := types.STREAM_ID_GROUNDWORKS
	seedOptions(t, k, ctx, gw, 3)
	require.NoError(t, k.OptionSeq.Set(ctx, key(gw), 3))
	vkey := []byte("gwpos/01234567890123456789")
	alloc := func(id uint64) math.Int {
		o, err := k.Options.Get(ctx, optionKey(gw, id))
		require.NoError(t, err)
		return o.AmountAllocated
	}
	ow := func(id uint64, w int64) types.OptionWeight {
		return types.OptionWeight{OptionId: id, Weight: math.NewInt(w)}
	}

	require.NoError(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(1, 7), ow(3, 1_000_003), ow(2, 0)}))
	v, err := k.Voters.Get(ctx, voterKey(gw, vkey))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_010), v.Weight)
	require.Len(t, v.OptionWeights, 2, "zero weights are dropped")
	require.Equal(t, math.NewInt(7), alloc(1))
	require.True(t, alloc(2).IsZero())
	require.Equal(t, math.NewInt(1_000_003), alloc(3))
	tw, err := k.getTotalWeight(ctx, gw)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_010), tw)

	// Refused: a duplicate option, a negative weight.
	require.ErrorIs(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(1, 1), ow(1, 2)}), types.ErrBadPercentages)
	require.ErrorIs(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(1, -1)}), types.ErrBadPercentages)

	// Replace: the old weights come off exactly.
	require.NoError(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(2, 5)}))
	require.True(t, alloc(1).IsZero())
	require.Equal(t, math.NewInt(5), alloc(2))
	require.True(t, alloc(3).IsZero())
	// ResyncVoter leaves a weighted voter alone.
	require.NoError(t, k.ResyncVoter(ctx, gw, vkey))
	require.Equal(t, math.NewInt(5), alloc(2))

	// Export validates and re-imports it.
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())

	// Clear.
	require.NoError(t, k.SetWeightedVoter(ctx, gw, vkey, nil))
	_, err = k.Voters.Get(ctx, voterKey(gw, vkey))
	require.Error(t, err)
	tw, err = k.getTotalWeight(ctx, gw)
	require.NoError(t, err)
	require.True(t, tw.IsZero())

	// A reset retires it: the next write does not subtract it again.
	require.NoError(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(1, 9)}))
	_, err = k.resetAllocations(ctx, gw)
	require.NoError(t, err)
	epoch, err := k.StreamEpoch(ctx, gw)
	require.NoError(t, err)
	require.EqualValues(t, 1, epoch)
	require.NoError(t, k.SetWeightedVoter(ctx, gw, vkey, []types.OptionWeight{ow(1, 4)}))
	require.Equal(t, math.NewInt(4), alloc(1))
}

func TestWeightedVoterGenesisValidation(t *testing.T) {
	ow := func(id uint64, w int64) types.OptionWeight {
		return types.OptionWeight{OptionId: id, Weight: math.NewInt(w)}
	}
	for name, v := range map[string]types.Voter{
		"both":      {Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}}, OptionWeights: []types.OptionWeight{ow(1, 1)}, Weight: math.NewInt(1)},
		"bad sum":   {OptionWeights: []types.OptionWeight{ow(1, 1), ow(2, 2)}, Weight: math.NewInt(4)},
		"zero":      {OptionWeights: []types.OptionWeight{ow(1, 0)}, Weight: math.NewInt(0)},
		"duplicate": {OptionWeights: []types.OptionWeight{ow(1, 1), ow(1, 1)}, Weight: math.NewInt(2)},
	} {
		e := newTestEnv(t)
		k, ctx := e.k, e.ctx
		require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
		seedOptions(t, k, ctx, types.STREAM_ID_GROUNDWORKS, 2)
		require.NoError(t, k.OptionSeq.Set(ctx, key(types.STREAM_ID_GROUNDWORKS), 2))
		require.NoError(t, k.Voters.Set(ctx, voterKey(types.STREAM_ID_GROUNDWORKS, []byte("gwpos/01234567890123456789")), v))
		gs, err := k.ExportGenesis(ctx)
		require.NoError(t, err)
		require.Error(t, gs.Validate(), name)
	}
}

// Audit 6 D-L-A1: an option pruned after a voter named it leaves the export
// (the runtime skips it); validation still refuses a hand-written genesis
// naming one.
func TestExportDropsPrunedOptions(t *testing.T) {
	ow := func(id uint64, w int64) types.OptionWeight {
		return types.OptionWeight{OptionId: id, Weight: math.NewInt(w)}
	}
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	gw := types.STREAM_ID_GROUNDWORKS
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	seedOptions(t, k, ctx, gw, 2)
	require.NoError(t, k.OptionSeq.Set(ctx, key(gw), 2))
	require.NoError(t, k.Voters.Set(ctx, voterKey(gw, []byte("gwpos/01234567890123456789")),
		types.Voter{OptionWeights: []types.OptionWeight{ow(1, 1), ow(9, 2)}, Weight: math.NewInt(3)}))
	require.NoError(t, k.Voters.Set(ctx, voterKey(gw, []byte("gwpos/98765432109876543210")),
		types.Voter{OptionWeights: []types.OptionWeight{ow(9, 2)}, Weight: math.NewInt(2)}))
	require.NoError(t, k.Voters.Set(ctx, voterKey(gw, []byte("addr-a")),
		types.Voter{Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 60}, {OptionId: 9, Percent: 40}}, Weight: math.NewInt(10)}))
	require.NoError(t, k.Voters.Set(ctx, voterKey(gw, []byte("addr-b")),
		types.Voter{Percentages: []types.AllocationWeight{{OptionId: 9, Percent: 100}}, Weight: math.NewInt(10)}))
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	var voters []types.VoterEntry
	for _, st := range gs.Streams {
		if st.Stream == gw {
			voters = st.Voters
		}
	}
	require.Len(t, voters, 2, "the voters naming only the pruned option are gone")
	for _, v := range voters {
		for _, w := range v.Voter.OptionWeights {
			require.NotEqual(t, uint64(9), w.OptionId)
		}
		for _, w := range v.Voter.Percentages {
			require.NotEqual(t, uint64(9), w.OptionId)
		}
		if len(v.Voter.OptionWeights) > 0 {
			require.Equal(t, math.NewInt(1), v.Voter.Weight)
		}
	}
	for i := range gs.Streams {
		if gs.Streams[i].Stream == gw {
			for j := range gs.Streams[i].Voters {
				if len(gs.Streams[i].Voters[j].Voter.OptionWeights) > 0 {
					gs.Streams[i].Voters[j].Voter = types.Voter{OptionWeights: []types.OptionWeight{ow(9, 1)}, Weight: math.NewInt(1)}
				}
			}
		}
	}
	require.ErrorContains(t, gs.Validate(), "does not exist")
}
