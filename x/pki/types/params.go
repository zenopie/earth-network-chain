package types

// NewParams creates a new Params instance. The module has no parameters.
func NewParams() Params { return Params{} }

// DefaultParams returns default module parameters.
func DefaultParams() Params { return NewParams() }

// Validate validates the params.
func (p Params) Validate() error { return nil }
