package types

import (
	"encoding/hex"
	"fmt"
)

// DefaultGenesis returns the default genesis state.
func DefaultGenesis() *GenesisState {
	return &GenesisState{Params: DefaultParams()}
}

// Validate performs basic genesis state validation.
func (gs GenesisState) Validate() error {
	seen := make(map[string]struct{}, len(gs.RevokedDscs))
	for _, id := range gs.RevokedDscs {
		if len(id) == 0 {
			return fmt.Errorf("revoked dsc entry is empty")
		}
		h := hex.EncodeToString(id)
		if _, dup := seen[h]; dup {
			return fmt.Errorf("dsc %s is revoked twice", h)
		}
		seen[h] = struct{}{}
	}
	return gs.Params.Validate()
}
