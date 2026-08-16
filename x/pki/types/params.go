package types

// NewParams creates a new Params instance. The module currently has no
// parameters: tree_depth and root_history_size configured the DSC-registry
// Merkle tree, which registration no longer uses.
func NewParams() Params { return Params{} }

// DefaultParams returns default module parameters.
func DefaultParams() Params { return NewParams() }

// Validate validates the params.
func (p Params) Validate() error { return nil }
