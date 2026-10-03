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
	// DefaultStakeRootWindowSeconds is two weeks, as the shielded pool's.
	DefaultStakeRootWindowSeconds uint64 = 14 * 24 * 60 * 60
)

// DefaultMinPosition is 100 ERTH of derth: filling all max_positions slots
// (squatting them) then takes 1,000,000 ERTH locked.
var DefaultMinPosition = math.NewInt(100_000_000)

// DefaultMinDelegation is 1 ERTH: the smallest private delegation, and the
// least derth one mints.
var DefaultMinDelegation = math.NewInt(1_000_000)

// DefaultParams returns the default parameters.
func DefaultParams() Params {
	return Params{EpochSeconds: DefaultEpochSeconds, MinPosition: DefaultMinPosition, MaxPositions: DefaultMaxPositions,
		StakeRootWindowSeconds: DefaultStakeRootWindowSeconds, MinDelegation: DefaultMinDelegation}
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
	if p.StakeRootWindowSeconds == 0 {
		return errors.New("stake_root_window_seconds must be positive")
	}
	if p.MinDelegation.IsNil() || !p.MinDelegation.IsPositive() {
		return errors.New("min_delegation must be positive")
	}
	return nil
}

// MaxEpochUnbondings is the most SDK unbonding entries the module can hold
// at once for one validator with these params and an unbonding time of
// unbondingSeconds: one per epoch-end sweep over the unbonding time, plus one
// for a sweep already under way (epoch.go). x/staking's max_entries must
// be at least this, or an epoch's undelegation would be refused.
func (p Params) MaxEpochUnbondings(unbondingSeconds uint64) uint64 {
	if p.EpochSeconds == 0 {
		return ^uint64(0)
	}
	return (unbondingSeconds+p.EpochSeconds-1)/p.EpochSeconds + 1
}
