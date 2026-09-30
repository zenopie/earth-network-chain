package types

import (
	"errors"

	"cosmossdk.io/math"
)

const (
	// DefaultEpochSeconds is one day.
	DefaultEpochSeconds uint64 = 24 * 60 * 60
	// DefaultMaxPositions bounds the positions re-weighed every epoch.
	DefaultMaxPositions uint64 = 10_000
)

// DefaultMinPosition is 1 ERTH of derth.
var DefaultMinPosition = math.NewInt(1_000_000)

// DefaultParams returns the default parameters.
func DefaultParams() Params {
	return Params{EpochSeconds: DefaultEpochSeconds, MinPosition: DefaultMinPosition, MaxPositions: DefaultMaxPositions}
}

// Validate validates the params.
func (p Params) Validate() error {
	if p.EpochSeconds == 0 {
		return errors.New("epoch_seconds must be positive")
	}
	if p.EpochSeconds > 30*24*60*60 {
		return errors.New("epoch_seconds must be at most 30 days")
	}
	if p.MinPosition.IsNil() || !p.MinPosition.IsPositive() {
		return errors.New("min_position must be positive")
	}
	if p.MaxPositions == 0 {
		return errors.New("max_positions must be positive")
	}
	return nil
}
