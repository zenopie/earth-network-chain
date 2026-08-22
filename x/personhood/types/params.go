package types

import (
	"errors"
	"fmt"
	"sort"
)

// NewParams creates a new Params instance.
func NewParams(verifyingKeys map[string][]byte, validitySeconds uint64) Params {
	return Params{
		VerifyingKeys:               verifyingKeys,
		RegistrationValiditySeconds: validitySeconds,
	}
}

// DefaultParams returns a default set of parameters. No verifying keys are
// configured until governance adds the passport register-circuit keys, so
// registration is disabled by default.
func DefaultParams() Params {
	// nil (not an empty map) so it matches the value after a proto round-trip,
	// which marshals an empty map to nothing and unmarshals back to nil.
	p := NewParams(nil, DefaultRegistrationValiditySeconds)
	p.CurrentDateMaxSkewSeconds = DefaultCurrentDateMaxSkewSeconds
	p.RegistrationSweepLimit = DefaultRegistrationSweepLimit
	p.ProofVerificationGas = DefaultProofVerificationGas
	p.DscVerificationGas = DefaultDscVerificationGas
	p.BuybackTwapWindowSeconds = DefaultBuybackTwapWindowSeconds
	p.BuybackMaxDeviationBps = DefaultBuybackMaxDeviationBps
	p.BuybackMaxAccrualSeconds = DefaultBuybackMaxAccrualSeconds
	p.DscDailyRegistrationFloor = DefaultDscDailyRegistrationFloor
	p.DscDailyRegistrationShareBps = DefaultDscDailyRegistrationShareBps
	p.CountryDailyRegistrationFloor = DefaultCountryDailyRegistrationFloor
	p.CountryDailyRegistrationShareBps = DefaultCountryDailyRegistrationShareBps
	return p
}

// RegistrationSweepLimitOrDefault returns the shared per-block retirement budget.
func (p Params) RegistrationSweepLimitOrDefault() int {
	if p.RegistrationSweepLimit == 0 {
		return DefaultRegistrationSweepLimit
	}
	return int(p.RegistrationSweepLimit)
}

// DailyRegistrationCap resolves one of the rate caps against the network's
// previous completed day: the larger of a fixed floor and a share of what the
// whole network registered yesterday.
//
// Taking the larger is what makes the pair work. The floor alone stops meaning
// anything once the network outgrows it; the share alone is meaningless at
// genesis, where a handful of registrations make any signer look dominant. The
// result never falls below the floor, so adding the share term can only widen
// the cap — it can never turn into a tighter limit than governance set.
func DailyRegistrationCap(floor, shareBps uint64, networkPreviousDay uint64) uint64 {
	share := networkPreviousDay * shareBps / BpsDenominator
	if share > floor {
		return share
	}
	return floor
}

// DscDailyCap returns the per-signer registration cap for the current day.
func (p Params) DscDailyCap(networkPreviousDay uint64) uint64 {
	floor := p.DscDailyRegistrationFloor
	if floor == 0 {
		floor = DefaultDscDailyRegistrationFloor
	}
	share := p.DscDailyRegistrationShareBps
	if share == 0 {
		share = DefaultDscDailyRegistrationShareBps
	}
	return DailyRegistrationCap(floor, share, networkPreviousDay)
}

// CountryDailyCap returns the per-country registration cap for the current day.
func (p Params) CountryDailyCap(networkPreviousDay uint64) uint64 {
	floor := p.CountryDailyRegistrationFloor
	if floor == 0 {
		floor = DefaultCountryDailyRegistrationFloor
	}
	share := p.CountryDailyRegistrationShareBps
	if share == 0 {
		share = DefaultCountryDailyRegistrationShareBps
	}
	return DailyRegistrationCap(floor, share, networkPreviousDay)
}

// The five knobs below all read zero as "unset, use the compiled-in default"
// rather than as a literal zero.
//
// Zero is the value state arrives with when a chain upgrade adds a field to
// Params: the existing stored Params decode with the new field at its zero
// value, and no migration runs unless one is written. For every one of these,
// taking that zero literally is the dangerous reading — no gas charged for the
// verifier, no averaging window, no deviation bound, no accrual cap. Falling
// back to the default keeps an un-migrated upgrade safe, and governance can
// still set any of them explicitly.
//
// This is the opposite of the choice made for current_date_max_skew_seconds,
// which fails closed instead. The difference is which way the failure points:
// there, zero disables a check and the safe response is to stop registering;
// here, zero would disable a bound whose absence is itself the hazard, so the
// safe response is to apply the default and keep running.

// ProofVerificationGasOrDefault returns the gas to charge per proof verification.
func (p Params) ProofVerificationGasOrDefault() uint64 {
	if p.ProofVerificationGas == 0 {
		return DefaultProofVerificationGas
	}
	return p.ProofVerificationGas
}

// DscVerificationGasOrDefault returns the gas to charge per DSC chain verification.
func (p Params) DscVerificationGasOrDefault() uint64 {
	if p.DscVerificationGas == 0 {
		return DefaultDscVerificationGas
	}
	return p.DscVerificationGas
}

// BuybackTwapWindowSecondsOrDefault returns the buyback's minimum averaging window.
func (p Params) BuybackTwapWindowSecondsOrDefault() int64 {
	if p.BuybackTwapWindowSeconds == 0 {
		return DefaultBuybackTwapWindowSeconds
	}
	return int64(p.BuybackTwapWindowSeconds)
}

// BuybackMaxDeviationBpsOrDefault returns the buyback's spot-vs-TWAP tolerance.
func (p Params) BuybackMaxDeviationBpsOrDefault() int64 {
	if p.BuybackMaxDeviationBps == 0 {
		return DefaultBuybackMaxDeviationBps
	}
	return int64(p.BuybackMaxDeviationBps)
}

// BuybackMaxAccrualSecondsOrDefault returns the buyback's catch-up cap.
func (p Params) BuybackMaxAccrualSecondsOrDefault() int64 {
	if p.BuybackMaxAccrualSeconds == 0 {
		return DefaultBuybackMaxAccrualSeconds
	}
	return int64(p.BuybackMaxAccrualSeconds)
}

// Validate validates the set of params. Verifying keys are opaque Barretenberg
// UltraHonk binary blobs (`bb write_vk`); we only check they are non-empty here.
// Full structural validation happens at verify time (CGo verifier), which the
// types package intentionally does not depend on.
func (p Params) Validate() error {
	// Sorted rather than ranging the map directly: with two empty keys, map order
	// would decide which one the error names, so the same params would produce
	// different messages on different nodes. Error text is not part of the
	// results hash, so this is reproducibility rather than consensus — but it is
	// the same pattern that broke consensus in Params.Marshal, so it is not left
	// to chance.
	algos := make([]string, 0, len(p.VerifyingKeys))
	for algo := range p.VerifyingKeys {
		algos = append(algos, algo)
	}
	sort.Strings(algos)
	for _, algo := range algos {
		if len(p.VerifyingKeys[algo]) == 0 {
			return fmt.Errorf("verifying key for %q is empty", algo)
		}
	}
	// Zero is not "no tolerance", it is "no check": the skew comparison is the
	// only thing tying the prover-supplied current_date to real time, and
	// without it any expired passport proves out against a backdated date.
	if p.CurrentDateMaxSkewSeconds == 0 {
		return errors.New("current_date_max_skew_seconds must be positive: 0 leaves passport expiry unenforced")
	}
	// Governance may leave these at zero to take the default (see the
	// *OrDefault accessors), but it may not set them to a value that is present
	// and wrong. Only the upper bound needs policing: a deviation tolerance at
	// or above 100% admits any price at all, which is the unguarded buyback this
	// bound exists to prevent.
	if p.BuybackMaxDeviationBps >= BpsDenominator {
		return fmt.Errorf(
			"buyback_max_deviation_bps must be below %d: %d admits any price the pool can be pushed to",
			BpsDenominator, p.BuybackMaxDeviationBps)
	}
	// A share at or above 100% of the network's registrations is not a bound at
	// all: one signer could account for everything and still be under it. Zero is
	// allowed and means "take the default" (see the caps above), so only the
	// upper end needs policing.
	if p.DscDailyRegistrationShareBps >= BpsDenominator {
		return fmt.Errorf(
			"dsc_daily_registration_share_bps must be below %d: %d lets one signer account for every registration",
			BpsDenominator, p.DscDailyRegistrationShareBps)
	}
	if p.CountryDailyRegistrationShareBps >= BpsDenominator {
		return fmt.Errorf(
			"country_daily_registration_share_bps must be below %d: %d lets one country account for every registration",
			BpsDenominator, p.CountryDailyRegistrationShareBps)
	}
	return nil
}
