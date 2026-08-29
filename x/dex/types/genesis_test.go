package types_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/types"
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
			desc:     "valid genesis state",
			genState: &types.GenesisState{Params: types.DefaultParams(), PoolMap: []types.Pool{{PoolId: 0}, {PoolId: 1}}},
			valid:    true,
		}, {
			desc: "duplicated pool",
			genState: &types.GenesisState{
				PoolMap: []types.Pool{
					{
						PoolId: 0,
					},
					{
						PoolId: 0,
					},
				},
			},
			valid: false,
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

// A genesis carrying a pool whose spoke side is an LP share denom must fail to
// validate. Starting on one is a chain that halts on its first EndBlocker.
func TestGenesisRejectsAnLpShareSpokeDenom(t *testing.T) {
	gs := types.DefaultGenesis()
	gs.PoolMap = []types.Pool{{
		PoolId:       1,
		ReserveErth:  sdk.NewInt64Coin("uerth", 1_000),
		ReserveToken: sdk.NewInt64Coin(types.LPShareDenom(2), 1_000),
		VolumeWeight: math.ZeroInt(),
	}}
	require.ErrorContains(t, gs.Validate(), "lp share denom")
}
