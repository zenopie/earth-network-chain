package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// TestSwapCountsTheBurnedHalfOfTheFee: half of every swap fee is destroyed, and
// this is the only burn on the chain that happens inside a transaction. It is
// still counted rather than left to be scraped from the event, because a total
// assembled from two different sources — some from state, some from the indexer
// — would disagree with itself the first time a node pruned.
func TestSwapCountsTheBurnedHalfOfTheFee(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)

	trader := sdk.AccAddress("trader______________")
	_, err := k.SwapExactIn(ctx, trader, sdk.NewInt64Coin("uerth", 100_000), "utok", math.ZeroInt())
	require.NoError(t, err)

	counted := bank.counted[earthtypes.SourceSwapFee]
	require.False(t, counted.AmountOf("uerth").IsZero(), "the swap burned nothing to count")
	require.Equal(t, bank.burned.AmountOf("uerth"), counted.AmountOf("uerth"),
		"the counter and the bank must agree on what left the supply")
}

// TestTwoHopSwapCountsBothBurns: a tokenA -> ERTH -> tokenB trade charges the
// fee twice, and both halves are burned in one call. The counter has to see the
// total, not the last hop.
func TestTwoHopSwapCountsBothBurns(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)
	seedFundedPool(t, k, ctx, bank, 2, 1_000_000, 1_000_000, 1_000)

	trader := sdk.AccAddress("trader______________")
	_, err := k.SwapExactIn(ctx, trader, sdk.NewInt64Coin("utok", 100_000), "utok2", math.ZeroInt())
	require.NoError(t, err)

	counted := bank.counted[earthtypes.SourceSwapFee]
	require.Equal(t, bank.burned.AmountOf("uerth"), counted.AmountOf("uerth"))
}
