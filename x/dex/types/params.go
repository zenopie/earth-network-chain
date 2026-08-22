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

// DefaultVolumeDepthCapPerDay is how much volume a pool may count toward LP
// rewards each day, as a multiple of its ERTH reserve: 2x.
//
// Sized so that honest pools never meet it and manufactured volume always does.
// A pool's ERTH reserve is half its value, so 2x the ERTH side is roughly one
// turn of the whole pool per day — brisk for a real market, and well above what
// all but the hottest pairs sustain. Faking volume up to the cap, by contrast,
// costs the burned half of the fee on every unit: at a 0.3% fee that is 0.3% of
// the pool's ERTH reserve burned per day, over 100% a year, which only pays if
// the LP reward yield exceeds it. The cap is what forces that comparison to
// happen against real deposited depth rather than against nothing at all.
const DefaultVolumeDepthCapPerDay = 2

// VolumeDepthCapPerDayOrDefault returns the volume cap multiple, reading zero as
// "unset, use the default" rather than as a cap of zero — which would leave
// every pool unable to earn anything. See the proto comment.
func (p Params) VolumeDepthCapPerDayOrDefault() uint64 {
	if p.VolumeDepthCapPerDay == 0 {
		return DefaultVolumeDepthCapPerDay
	}
	return p.VolumeDepthCapPerDay
}

// NewParams creates a new Params instance.
func NewParams(swapFee math.LegacyDec, lpUnbondingSeconds uint64) Params {
	return Params{
		SwapFee:              swapFee,
		LpUnbondingSeconds:   lpUnbondingSeconds,
		VolumeDepthCapPerDay: DefaultVolumeDepthCapPerDay,
	}
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
