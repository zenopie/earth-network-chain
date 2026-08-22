package types_test

import (
	"strings"
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// An import must not carry a description a message could not have created.
// Otherwise the cap is a formality: an oversized option can be written into a
// genesis file by hand, and from then on it is decoded in every block.
func TestGenesisRejectsAnOversizedDescription(t *testing.T) {
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		Streams: []types.StreamState{{
			Stream:      types.STREAM_ID_GROUNDWORKS,
			RewardIndex: math.ZeroInt(),
			TotalWeight: math.ZeroInt(),
			OptionSeq:   1,
			Options: []types.AllocationOption{{
				Id:              1,
				Stream:          types.STREAM_ID_GROUNDWORKS,
				Description:     strings.Repeat("a", types.MaxDescriptionLen+1),
				Kind:            types.ALLOCATION_KIND_INTEGRATED,
				Handler:         types.HandlerLPRewards,
				AmountAllocated: math.ZeroInt(),
				LastRewardIndex: math.ZeroInt(),
				Accumulated:     math.ZeroInt(),
			}},
		}},
	}

	require.ErrorIs(t, gs.Validate(), types.ErrDescriptionTooLong)

	gs.Streams[0].Options[0].Description = strings.Repeat("a", types.MaxDescriptionLen)
	require.NoError(t, gs.Validate())
}
