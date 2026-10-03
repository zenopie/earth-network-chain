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
		"unknown":   {OptionWeights: []types.OptionWeight{ow(9, 1)}, Weight: math.NewInt(1)},
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
