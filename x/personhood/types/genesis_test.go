package types_test

import (
	"testing"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/stretchr/testify/require"
)

func TestGenesisState_Validate(t *testing.T) {
	tests := []struct {
		desc     string
		genState *types.GenesisState
		valid    bool
	}{
		{
			desc:     "default is valid",
			genState: types.DefaultGenesis(),
			valid:    true,
		},
		{
			// Zero-valued params leave current_date_max_skew_seconds at 0, which
			// would let a backdated proof register an expired passport. Genesis
			// has to name a tolerance explicitly.
			desc:     "zero params are rejected",
			genState: &types.GenesisState{},
			valid:    false,
		},
		{
			desc: "explicit skew is valid",
			genState: &types.GenesisState{
				Params: types.Params{
					RegistrationValiditySeconds: types.DefaultRegistrationValiditySeconds,
					CurrentDateMaxSkewSeconds:   types.DefaultCurrentDateMaxSkewSeconds,
				},
			},
			valid: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.genState.Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
