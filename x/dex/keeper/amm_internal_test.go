package keeper

import (
	"testing"

	"cosmossdk.io/math"
)

// TestSplitFeeRoundsToTheBurn pins the swap fee's rounding rule alongside the
// gas fee's (x/earth/keeper/fees_test.go): where a fee cannot be halved evenly
// the odd unit is destroyed rather than kept. One rule for every fee on the
// chain, so rounding never quietly favours a recipient over the burn.
func TestSplitFeeRoundsToTheBurn(t *testing.T) {
	for _, tc := range []struct{ fee, burn, pool int64 }{
		{0, 0, 0},
		{1, 1, 0}, // a single unit cannot be shared: it burns
		{2, 1, 1},
		{3, 2, 1},
		{4, 2, 2},
		{5, 3, 2},
		{999, 500, 499},
		{1_000, 500, 500},
	} {
		burn, pool := splitFee(math.NewInt(tc.fee))
		if !burn.Equal(math.NewInt(tc.burn)) || !pool.Equal(math.NewInt(tc.pool)) {
			t.Errorf("splitFee(%d) = (burn %s, pool %s), want (burn %d, pool %d)",
				tc.fee, burn, pool, tc.burn, tc.pool)
		}
		// The split must be exact at every parity: a fee that loses or invents a
		// unit is a supply bug, and it would be invisible at any realistic size.
		if got := burn.Add(pool); !got.Equal(math.NewInt(tc.fee)) {
			t.Errorf("splitFee(%d) does not conserve the fee: burn+pool = %s", tc.fee, got)
		}
		if burn.LT(pool) {
			t.Errorf("splitFee(%d) gave the burn the smaller half", tc.fee)
		}
	}
}
