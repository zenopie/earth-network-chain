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
	p.AddressOptionFee = DefaultAddressOptionFee
	p.CurrentDateMaxSkewSeconds = DefaultCurrentDateMaxSkewSeconds
	p.ExpirySweepLimit = DefaultExpirySweepLimit
	return p
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
	return nil
}
