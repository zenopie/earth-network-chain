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
	p.BuybackMaxTradeSeconds = DefaultBuybackMaxTradeSeconds
	p.DscDailyRegistrationFloor = DefaultDscDailyRegistrationFloor
	p.DscDailyRegistrationShareBps = DefaultDscDailyRegistrationShareBps
	p.CountryDailyRegistrationFloor = DefaultCountryDailyRegistrationFloor
	p.CountryDailyRegistrationShareBps = DefaultCountryDailyRegistrationShareBps
	p.NetworkDailyRegistrationFloor = DefaultNetworkDailyRegistrationFloor
	p.NetworkDailyRegistrationGrowthBps = DefaultNetworkDailyRegistrationGrowthBps
	p.IdentityRootWindowSeconds = DefaultIdentityRootWindowSeconds
	p.CaretakerVoteSeconds = DefaultCaretakerVoteSeconds
	p.HandleRenewalSeconds = DefaultHandleRenewalSeconds
	p.HandleLeaseSeconds = DefaultHandleLeaseSeconds
	return p
}

// BuybackMaxTradeSecondsOrDefault returns how many seconds of emission one
// buyback trade may spend.
func (p Params) BuybackMaxTradeSecondsOrDefault() int64 {
	if p.BuybackMaxTradeSeconds == 0 {
		return DefaultBuybackMaxTradeSeconds
	}
	return int64(p.BuybackMaxTradeSeconds)
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

// NetworkDailyCap returns the network-wide registration cap for the current day.
func (p Params) NetworkDailyCap(networkPreviousDay uint64) uint64 {
	floor := p.NetworkDailyRegistrationFloor
	if floor == 0 {
		floor = DefaultNetworkDailyRegistrationFloor
	}
	growth := p.NetworkDailyRegistrationGrowthBps
	if growth == 0 {
		growth = DefaultNetworkDailyRegistrationGrowthBps
	}
	return DailyRegistrationCap(floor, growth, networkPreviousDay)
}

// The *OrDefault accessors below all read zero as "unset, use the compiled-in default"
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

// IdentityRootWindowSecondsOrDefault returns how long a superseded identity
// root stays an anchor.
func (p Params) IdentityRootWindowSecondsOrDefault() int64 {
	if p.IdentityRootWindowSeconds == 0 {
		return DefaultIdentityRootWindowSeconds
	}
	return int64(p.IdentityRootWindowSeconds)
}

// CaretakerVoteSecondsOrDefault returns R, how long a caretaker split counts.
func (p Params) CaretakerVoteSecondsOrDefault() int64 {
	if p.CaretakerVoteSeconds == 0 {
		return DefaultCaretakerVoteSeconds
	}
	return int64(p.CaretakerVoteSeconds)
}

// HandleLeaseSecondsOrDefault is a handle's lease.
func (p Params) HandleLeaseSecondsOrDefault() int64 {
	if p.HandleLeaseSeconds == 0 {
		return DefaultHandleLeaseSeconds
	}
	return int64(p.HandleLeaseSeconds)
}

// HandleRenewalSecondsOrDefault is a lapsed handle's owner-only renewal
// period.
func (p Params) HandleRenewalSecondsOrDefault() int64 {
	if p.HandleRenewalSeconds == 0 {
		return DefaultHandleRenewalSeconds
	}
	return int64(p.HandleRenewalSeconds)
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
	// results hash, so this is reproducibility rather than consensus, but map
	// order is not left to chance anywhere in params.
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
	// Bounded so now + skew (the used-binding expiry) cannot overflow, and
	// because a skew of more than a year is no expiry check at all.
	if p.CurrentDateMaxSkewSeconds > MaxCurrentDateMaxSkewSeconds {
		return fmt.Errorf("current_date_max_skew_seconds must be at most a year")
	}
	// Zero takes the default. Otherwise at least one per sweep (runSweeps
	// reserves each later sweep a share; below this some get none) and at
	// most what one BeginBlock, on an infinite gas meter, can afford.
	if l := p.RegistrationSweepLimit; l != 0 && (l < MinRegistrationSweepLimit || l > MaxRegistrationSweepLimit) {
		return fmt.Errorf("registration_sweep_limit must be 0 (default) or in %d..%d, got %d",
			MinRegistrationSweepLimit, MaxRegistrationSweepLimit, l)
	}
	if p.RegistrationValiditySeconds == 0 || p.RegistrationValiditySeconds > MaxRegistrationValiditySeconds {
		return fmt.Errorf("registration_validity_seconds must be in 1..%d, got %d",
			uint64(MaxRegistrationValiditySeconds), p.RegistrationValiditySeconds)
	}
	// Each index names a different public input. Two pointing at the same one
	// make a single value serve as both — the nullifier doubling as the bound
	// address, say — and the check on each then constrains nothing the other
	// did not. Only once there is a verifying key: until then registration is
	// off, and the indexes sit at their zero defaults with nothing to index.
	indexes := map[uint32]string{}
	for _, idx := range []struct {
		name string
		at   uint32
	}{
		{"nullifier_index", p.NullifierIndex},
		{"dsc_key_index", p.DscKeyIndex},
		{"current_date_index", p.CurrentDateIndex},
		{"address_index", p.AddressIndex},
	} {
		if other, dup := indexes[idx.at]; dup && len(p.VerifyingKeys) > 0 {
			return fmt.Errorf("%s and %s both name public input %d", other, idx.name, idx.at)
		}
		indexes[idx.at] = idx.name
	}
	// Governance may leave these at zero to take the default (see the
	// *OrDefault accessors), but it may not set them to a value that is present
	// and wrong.
	//
	// Bounded so the activation arithmetic (now - R - window) stays far from
	// overflow, and so no zeroed leaf keeps proving for longer than a day.
	if p.IdentityRootWindowSeconds > SecondsPerDay {
		return fmt.Errorf("identity_root_window_seconds must be at most %d", SecondsPerDay)
	}
	if p.CaretakerVoteSeconds > 365*SecondsPerDay {
		return fmt.Errorf("caretaker_vote_seconds must be at most a year")
	}
	if p.HandleRenewalSeconds > 365*SecondsPerDay {
		return fmt.Errorf("handle_renewal_seconds must be at most a year")
	}
	if p.HandleLeaseSeconds > 2*365*SecondsPerDay {
		return fmt.Errorf("handle_lease_seconds must be at most two years")
	}
	// Only the upper bound needs policing: a deviation tolerance at or above
	// 100% admits any price at all, which is the unguarded buyback this bound
	// exists to prevent.
	if p.BuybackMaxDeviationBps >= BpsDenominator {
		return fmt.Errorf(
			"buyback_max_deviation_bps must be below %d: %d admits any price the pool can be pushed to",
			BpsDenominator, p.BuybackMaxDeviationBps)
	}
	// window <= per-trade cap <= accrual cap, on the values in force (each
	// zero means its default). A per-trade cap below one window would let
	// the backlog grow faster than it is bought; above the accrual cap it
	// bounds nothing. Checked on the effective values, not only when
	// buyback_max_trade_seconds is set: a zero cap (default one hour) with a
	// window raised past an hour was accepted (audit 4, C5), and so was a
	// window longer than the accrual cap.
	//
	// The trade fires in the first block after the window fills, a moment
	// anyone can compute; the TWAP gate (spot within max deviation of the
	// window's average) is what makes that moment unprofitable to trade
	// against, not its secrecy. Randomising the block from the block hash was
	// considered and not done: the proposer chooses the hash's inputs, so it
	// would hand the proposer the timing it is meant to hide.
	window := p.BuybackTwapWindowSecondsOrDefault()
	trade := p.BuybackMaxTradeSecondsOrDefault()
	accrual := p.BuybackMaxAccrualSecondsOrDefault()
	if trade < window {
		return fmt.Errorf("buyback_max_trade_seconds (%d) must be at least buyback_twap_window_seconds (%d)", trade, window)
	}
	if trade > accrual {
		return fmt.Errorf("buyback_max_trade_seconds (%d) must be at most buyback_max_accrual_seconds (%d)", trade, accrual)
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
