package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// Audit 4 C8: AdvanceIndexTo settles a stream up to a past time (a lapsed
// lease's expiry), so clearing the lapsed voter after it gives the lapsed
// weight none of the emission between its expiry and the sweep.
func TestAudit4AdvanceIndexToSettlesUpToExpiry(t *testing.T) {
	e := newTestEnv(t)
	k := e.k
	ctx := e.ctx.WithBlockTime(time.Unix(1_000_000, 0))
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesis()))
	acc, _ := e.addr("caretaker")
	vote := []types.AllocationWeight{{OptionId: 1, Percent: 100}}
	require.NoError(t, k.SetVoterSplit(ctx, types.STREAM_ID_CARETAKER, acc, vote, math.NewInt(types.HumanVoterWeight)))
	start, err := k.getRewardIndex(ctx, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)

	// Expired at +10s, swept at +100s.
	later := ctx.WithBlockTime(time.Unix(1_000_100, 0))
	require.NoError(t, k.AdvanceIndexTo(later, types.STREAM_ID_CARETAKER, 1_000_010))
	at10, err := k.getRewardIndex(later, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)
	require.NoError(t, k.ClearVoter(later, types.STREAM_ID_CARETAKER, acc))
	require.NoError(t, k.AdvanceIndex(later, types.STREAM_ID_CARETAKER))
	end, err := k.getRewardIndex(later, types.STREAM_ID_CARETAKER)
	require.NoError(t, err)

	perWeight := func(secs int64) math.Int {
		return math.NewInt(types.EmissionPerSecond * secs).Mul(indexPrecision).Quo(math.NewInt(types.HumanVoterWeight))
	}
	require.Equal(t, perWeight(10), at10.Sub(start), "settled up to the expiry")
	require.Equal(t, at10, end, "nothing accrues to the cleared weight after it (no voters left)")

	// A time at or before the last settlement is a no-op; a future one is
	// clamped to the block.
	require.NoError(t, k.AdvanceIndexTo(later, types.STREAM_ID_CARETAKER, 1_000_005))
	require.NoError(t, k.AdvanceIndexTo(later, types.STREAM_ID_CARETAKER, 2_000_000))
}
