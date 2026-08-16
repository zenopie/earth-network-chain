package types

// NewParams creates a new Params instance.
func NewParams(addressOptionFee uint64) Params {
	return Params{AddressOptionFee: addressOptionFee}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(DefaultAddressOptionFee)
}

// Validate validates the set of params.
func (p Params) Validate() error {
	return nil
}
