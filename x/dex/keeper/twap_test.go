package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// at returns ctx moved forward by d seconds, which is how these tests let the
// price accumulator earn time without running blocks.
func at(ctx sdk.Context, d time.Duration) sdk.Context {
	return ctx.WithBlockTime(ctx.BlockTime().Add(d))
}

// TestTwapAveragesTimeAtPrice is the accumulator's basic contract: the
// difference between two readings, over the seconds between them, is the price
// the pool held for that stretch.
func TestTwapAveragesTimeAtPrice(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000) // price 1.0

	cum0, spot0, t0, err := k.TwapObservation(ctx, "utok")
	require.NoError(t, err)
	require.Equal(t, math.LegacyOneDec(), spot0)

	later := at(ctx, 10*time.Minute)
	cum1, _, t1, err := k.TwapObservation(later, "utok")
	require.NoError(t, err)

	window := t1 - t0
	require.EqualValues(t, 600, window)

	twap := cum1.Sub(cum0).QuoInt64(window)
	require.Equal(t, math.LegacyOneDec(), twap, "a pool held at 1.0 for ten minutes averages 1.0")
}

// TestTwapQuietPoolStillAverages pins the reason reading the oracle writes.
// A pool that nobody trades is still sitting at a price, and the average over
// that stretch has to be that price rather than zero — otherwise every consumer
// of the oracle is disabled precisely when the pool is calmest.
func TestTwapQuietPoolStillAverages(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 2_000_000, 1_000_000, 0) // price 2.0, zero volume

	cum0, _, t0, err := k.TwapObservation(ctx, "utok")
	require.NoError(t, err)

	// No swaps at all in between.
	later := at(ctx, time.Hour)
	cum1, _, t1, err := k.TwapObservation(later, "utok")
	require.NoError(t, err)

	twap := cum1.Sub(cum0).QuoInt64(t1 - t0)
	require.Equal(t, math.LegacyNewDec(2), twap, "an untraded pool averages the price it sat at")
}

// TestTwapResistsSingleBlockManipulation is the property the buyback depends on:
// a large swap moves the spot price immediately but barely moves an average that
// has a long window behind it. Manipulating what the buyback prices against
// therefore means holding the price for real time, not for one block.
func TestTwapResistsSingleBlockManipulation(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000) // price 1.0

	cum0, _, t0, err := k.TwapObservation(ctx, "utok")
	require.NoError(t, err)

	// An hour passes quietly at 1.0, then an attacker buys the token hard.
	later := at(ctx, time.Hour)
	trader := sdk.AccAddress("attacker____________")
	_, err = k.SwapExactIn(later, trader, sdk.NewInt64Coin("uerth", 400_000), "utok", math.ZeroInt())
	require.NoError(t, err)

	cum1, spot, t1, err := k.TwapObservation(later, "utok")
	require.NoError(t, err)
	twap := cum1.Sub(cum0).QuoInt64(t1 - t0)

	require.True(t, spot.GT(math.LegacyNewDecWithPrec(15, 1)),
		"the swap should have moved spot well above 1.5, got %s", spot)
	require.Equal(t, math.LegacyOneDec(), twap,
		"the average over the hour before the swap is untouched by it, got %s", twap)

	// And the deviation the buyback gates on is large, which is the signal.
	require.True(t, spot.Quo(twap).GT(math.LegacyNewDecWithPrec(102, 2)),
		"spot/twap should breach a 2%% band, got %s", spot.Quo(twap))
}

// TestTwapFollowsAPriceThatIsActuallyHeld is the other half: the gate must not
// be permanent. A price the market genuinely moves to is absorbed by the average
// once it has been held for a window, so a real revaluation stops being read as
// manipulation.
func TestTwapFollowsAPriceThatIsActuallyHeld(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)

	trader := sdk.AccAddress("buyer_______________")
	_, err := k.SwapExactIn(ctx, trader, sdk.NewInt64Coin("uerth", 400_000), "utok", math.ZeroInt())
	require.NoError(t, err)

	_, spot, _, err := k.TwapObservation(ctx, "utok")
	require.NoError(t, err)

	// Take the window entirely after the move, so the average is of the new price.
	cum0, _, t0, err := k.TwapObservation(ctx, "utok")
	require.NoError(t, err)
	later := at(ctx, 10*time.Minute)
	cum1, spotLater, t1, err := k.TwapObservation(later, "utok")
	require.NoError(t, err)

	twap := cum1.Sub(cum0).QuoInt64(t1 - t0)
	require.Equal(t, spot, spotLater, "nothing traded, so spot is unchanged")
	require.Equal(t, spot, twap, "a price held for the whole window becomes the average")
}

// TestQuoteMatchesSwapAndDoesNotMutate pins the two things the buyback relies on
// when it turns a quote into min_out: the number is what the swap will actually
// return, and asking for it changes nothing.
func TestQuoteMatchesSwapAndDoesNotMutate(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 5_000_000, 5_000_000, 1_000)

	poolBefore, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	mintedBefore := bank.minted

	in := math.NewInt(250_000)
	quoted, err := k.QuoteHubToToken(ctx, "utok", in)
	require.NoError(t, err)
	require.True(t, quoted.IsPositive())

	poolAfter, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, poolBefore, poolAfter, "quoting must not move the reserves")
	require.Equal(t, mintedBefore, bank.minted, "quoting must not mint")

	trader := sdk.AccAddress("trader______________")
	got, err := k.SwapExactIn(ctx, trader, sdk.NewCoin("uerth", in), "utok", math.ZeroInt())
	require.NoError(t, err)
	require.Equal(t, quoted, got.Amount, "the quote must be exactly what the swap returns")
}
