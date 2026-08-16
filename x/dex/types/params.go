package types

import (
	"fmt"

	"cosmossdk.io/math"
)

// DefaultSwapFee is the default swap fee as a percentage: 0.3 means 0.3%.
var DefaultSwapFee = math.LegacyNewDecWithPrec(3, 1) // 0.3

// MaxSwapFee is the maximum allowed swap fee as a percentage: 10 means 10%.
var MaxSwapFee = math.LegacyNewDec(10)

// MaxLpUnbondingSeconds bounds how long governance may make providers wait for
// their liquidity: 90 days. Unbonding funds cannot be reclaimed early by anyone,
// so an over-long value is not a policy mistake that can be walked back — it
// strands every position already in flight.
const MaxLpUnbondingSeconds = 90 * 24 * 60 * 60

// NewParams creates a new Params instance.
func NewParams(swapFee math.LegacyDec, lpUnbondingSeconds uint64) Params {
	return Params{SwapFee: swapFee, LpUnbondingSeconds: lpUnbondingSeconds}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(DefaultSwapFee, DefaultLpUnbondingSeconds)
}

// Validate validates the set of params.
func (p Params) Validate() error {
	if p.SwapFee.IsNil() {
		return fmt.Errorf("swap fee must not be nil")
	}
	if p.SwapFee.IsNegative() {
		return fmt.Errorf("swap fee %s%% must not be negative", p.SwapFee)
	}
	if p.SwapFee.GT(MaxSwapFee) {
		return fmt.Errorf("swap fee %s%% exceeds maximum of %s%%", p.SwapFee, MaxSwapFee)
	}
	// Zero is allowed and means withdrawals settle on the next block, which is
	// the behaviour from before unbonding existed.
	if p.LpUnbondingSeconds > MaxLpUnbondingSeconds {
		return fmt.Errorf("lp unbonding of %ds exceeds maximum of %ds",
			p.LpUnbondingSeconds, MaxLpUnbondingSeconds)
	}
	return nil
}
