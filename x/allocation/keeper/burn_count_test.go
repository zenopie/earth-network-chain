package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// TestOptionFeeIsCounted: opening an address option burns a fee, and that burn
// is a permanent reduction in supply like any other. It is small next to the
// pillar burns, which is exactly why it would be easy to leave uncounted and
// have the chain-wide total quietly drift low.
func TestOptionFeeIsCounted(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	_, alice := e.addr("alice")

	_, err := ms.AddAddressOption(e.ctx, &types.MsgAddAddressOption{
		Submitter: alice, Stream: types.STREAM_ID_GROUNDWORKS, Recipient: alice, Description: "grant",
	})
	require.NoError(t, err)

	counted := e.bank.counted[earthtypes.SourceAllocation]
	require.Equal(t, math.NewInt(types.DefaultAddressOptionFee), counted.AmountOf("uerth"))
	require.Equal(t, e.bank.burned.AmountOf("uerth"), counted.AmountOf("uerth"))
}

// TestForfeitedRewardsAreCounted: ERTH an option earned and nobody claimed was
// minted as it accrued, so pruning the option genuinely destroys supply that
// existed. Both of this module's burn paths report under one source, because a
// reader of the total does not care which of the two produced it.
func TestForfeitedRewardsAreCounted(t *testing.T) {
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

	e.advance(time.Hour)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: types.LPRewardsOptionID, Percent: 100}},
	})
	require.NoError(t, err)

	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.True(t, opt.Accumulated.IsPositive(), "the option should be owed something to forfeit")

	countedBefore := e.bank.counted[earthtypes.SourceAllocation].AmountOf("uerth")

	e.advance(time.Duration(graceSeconds+1) * time.Second)
	require.NoError(t, e.k.BeginBlocker(e.ctx))

	counted := e.bank.counted[earthtypes.SourceAllocation].AmountOf("uerth")
	require.Equal(t, opt.Accumulated, counted.Sub(countedBefore),
		"the forfeited balance is what the prune destroyed, so it is what the counter should show")
	require.Equal(t, e.bank.burned.AmountOf("uerth"), counted)
}
