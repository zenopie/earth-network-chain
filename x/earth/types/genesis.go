package types

import "fmt"

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params: DefaultParams(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	seen := make(map[string]struct{}, len(gs.Burned))
	for _, b := range gs.Burned {
		if b.Source == "" {
			return fmt.Errorf("burn total with empty source")
		}
		// A repeated source would import as one silently overwriting the other,
		// and the total it reported afterwards would be short by whichever lost.
		if _, dup := seen[b.Source]; dup {
			return fmt.Errorf("duplicate burn source %q", b.Source)
		}
		seen[b.Source] = struct{}{}

		// Validate covers the sorting, the duplicate denoms and the negative
		// amounts that would otherwise reach the store and make the counter
		// unreadable rather than merely wrong.
		if err := b.Amount.Validate(); err != nil {
			return fmt.Errorf("burn source %q: %w", b.Source, err)
		}
	}

	if gs.LastMintTime < 0 {
		return fmt.Errorf("last mint time is negative: %d", gs.LastMintTime)
	}

	return gs.Params.Validate()
}
