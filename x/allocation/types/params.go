package types

import "fmt"

const (
	// DefaultGroundworksLeaseSeconds is a Groundworks split's lease: a year,
	// as the caretaker split's.
	DefaultGroundworksLeaseSeconds uint64 = 365 * 24 * 60 * 60
	// MinGroundworksLeaseSeconds and MaxGroundworksLeaseSeconds bound it.
	MinGroundworksLeaseSeconds uint64 = 24 * 60 * 60
	MaxGroundworksLeaseSeconds uint64 = 2 * 365 * 24 * 60 * 60
)

// NewParams creates a new Params instance.
func NewParams(addressOptionFee uint64) Params {
	return Params{AddressOptionFee: addressOptionFee}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(DefaultAddressOptionFee)
}

// GroundworksLeaseSecondsOrDefault is groundworks_lease_seconds, the default
// when unset.
func (p Params) GroundworksLeaseSecondsOrDefault() uint64 {
	if p.GroundworksLeaseSeconds == 0 {
		return DefaultGroundworksLeaseSeconds
	}
	return p.GroundworksLeaseSeconds
}

// Validate validates the set of params.
func (p Params) Validate() error {
	if l := p.GroundworksLeaseSeconds; l != 0 && (l < MinGroundworksLeaseSeconds || l > MaxGroundworksLeaseSeconds) {
		return fmt.Errorf("groundworks_lease_seconds must be 0 (default) or %d..%d", MinGroundworksLeaseSeconds, MaxGroundworksLeaseSeconds)
	}
	return nil
}
