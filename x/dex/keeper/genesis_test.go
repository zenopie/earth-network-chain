package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"

	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params:  types.DefaultParams(),
		PoolMap: []types.Pool{{PoolId: 0}, {PoolId: 1}}}

	f := initFixture(t)
	err := f.keeper.InitGenesis(f.ctx, genesisState)
	require.NoError(t, err)
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.EqualExportedValues(t, genesisState.Params, got.Params)
	require.EqualExportedValues(t, genesisState.PoolMap, got.PoolMap)

}

// TestGenesisRoundTripsUnbondings guards liquidity that is mid-withdrawal at an
// export. Its shares sit escrowed on the module account, so dropping the entry
// would leave them outstanding with nobody able to redeem them.
func TestGenesisRoundTripsUnbondings(t *testing.T) {
	f := initFixture(t)

	addr, err := f.addressCodec.BytesToString(sdk.AccAddress("departing-provider__"))
	require.NoError(t, err)

	genesisState := types.GenesisState{
		Params:  types.DefaultParams(),
		PoolMap: []types.Pool{{PoolId: 1}},
		LpUnbondings: []types.LpUnbonding{{
			Address:        addr,
			PoolId:         1,
			Shares:         sdk.NewInt64Coin(types.LPShareDenom(1), 500),
			CompletionTime: 1_700_000_000,
		}},
	}

	require.NoError(t, f.keeper.InitGenesis(f.ctx, genesisState))
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.EqualExportedValues(t, genesisState.LpUnbondings, got.LpUnbondings)
}
