package types

import (
	"encoding/hex"
	"fmt"
)

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params: DefaultParams(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
//
// The nullifier checks are the point. A nullifier is what makes a passport
// unable to register twice, so a genesis carrying two registrations under one
// nullifier — or a registration with none at all — has already lost the property
// the module exists to provide, and it must be refused at import rather than
// discovered later.
func (gs GenesisState) Validate() error {
	seenNullifier := make(map[string]struct{}, len(gs.Registrations))
	seenAddr := make(map[string]struct{}, len(gs.Registrations))

	for _, reg := range gs.Registrations {
		if len(reg.Nullifier) == 0 {
			return fmt.Errorf("registration for %s has no nullifier", reg.Address)
		}
		n := hex.EncodeToString(reg.Nullifier)
		if _, dup := seenNullifier[n]; dup {
			return fmt.Errorf("nullifier %s is registered twice — one passport, two humans", n)
		}
		seenNullifier[n] = struct{}{}

		if reg.Address == "" {
			return fmt.Errorf("registration %s has no address", n)
		}
		// RegByAddr maps one address to one nullifier, so a second registration
		// at the same address would overwrite the first and strand it: counted
		// in RegCount, reachable by nullifier, invisible by address.
		if _, dup := seenAddr[reg.Address]; dup {
			return fmt.Errorf("address %s holds two registrations", reg.Address)
		}
		seenAddr[reg.Address] = struct{}{}

		if reg.RegisteredAt <= 0 {
			return fmt.Errorf("registration %s has no registration time; the expiry sweep "+
				"orders by it and would retire this one immediately", n)
		}
		if reg.LastAnmlClaim < 0 {
			return fmt.Errorf("registration %s has a negative last ANML claim", n)
		}
	}

	if gs.LastBuyback < 0 {
		return fmt.Errorf("last_buyback must not be negative")
	}

	return gs.Params.Validate()
}
