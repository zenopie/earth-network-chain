package types_test

import (
	"testing"

	"github.com/earth-network/earth/x/personhood/types"
)

func TestParamsValidateBoundsValidityAndIndexes(t *testing.T) {
	live := types.DefaultParams()
	live.VerifyingKeys = map[string][]byte{"lean": {1}}
	live.NullifierIndex, live.DscKeyIndex, live.CurrentDateIndex, live.AddressIndex = 2, 3, 0, 1
	if err := live.Validate(); err != nil {
		t.Fatalf("earth-1's layout: %v", err)
	}

	for name, mutate := range map[string]func(*types.Params){
		"zero validity":      func(p *types.Params) { p.RegistrationValiditySeconds = 0 },
		"unbounded validity": func(p *types.Params) { p.RegistrationValiditySeconds = 1 << 63 },
		"shared index":       func(p *types.Params) { p.AddressIndex = p.NullifierIndex },
	} {
		p := live
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// No verifying key, registration off: the zero indexes are fine.
	if err := types.DefaultParams().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}
