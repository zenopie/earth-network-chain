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
			// Reserves are spelled out because a pool with nil ones is no longer
			// valid: a nil math.Int survives import and panics on first use.
			desc: "valid genesis state",
			genState: &types.GenesisState{Params: types.DefaultParams(), PoolMap: []types.Pool{
				validPool(0), validPool(1),
			}},
			valid: true,
		}, {
			desc: "pool with nil reserves",
			genState: &types.GenesisState{Params: types.DefaultParams(), PoolMap: []types.Pool{
				{PoolId: 1},
			}},
			valid: false,
		}, {
			desc: "pool with a zero reserve",
			genState: &types.GenesisState{Params: types.DefaultParams(), PoolMap: []types.Pool{
				func() types.Pool {
					p := validPool(1)
					p.ReserveToken.Amount = math.ZeroInt()
					return p
				}(),
			}},
			valid: false,
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

// validPool is the smallest pool a genesis may now carry: real reserves on both
// sides, because nil ones panic rather than error the first time anything prices
// against them.
func validPool(id uint64) types.Pool {
	return types.Pool{
		PoolId:       id,
		ReserveErth:  sdk.NewInt64Coin("uerth", 1_000_000),
		ReserveToken: sdk.NewInt64Coin("uanml", 1_000_000),
		VolumeWeight: math.ZeroInt(),
	}
}

// The unbonding queue is reachable through genesis and had no validation at all.
// Each of these is an entry SweepMaturedUnbondings would trip over at maturity —
// in the EndBlocker, at a height nobody chose.
func TestGenesisRejectsMalformedLpUnbondings(t *testing.T) {
	const provider = "earth1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqjxq6ln"

	base := func() *types.GenesisState {
		gs := types.DefaultGenesis()
		gs.PoolMap = []types.Pool{validPool(1)}
		return gs
	}
	entry := func() types.LpUnbonding {
		return types.LpUnbonding{
			Address:        provider,
			PoolId:         1,
			Shares:         sdk.NewInt64Coin(types.LPShareDenom(1), 500),
			CompletionTime: 1_700_000_000,
		}
	}

	t.Run("a well-formed entry is accepted", func(t *testing.T) {
		gs := base()
		gs.LpUnbondings = []types.LpUnbonding{entry()}
		require.NoError(t, gs.Validate())
	})

	// Pool.Get errors, and the sweep used to return that error out of EndBlock.
	t.Run("pool does not exist", func(t *testing.T) {
		gs := base()
		e := entry()
		e.PoolId = 99
		e.Shares = sdk.NewInt64Coin(types.LPShareDenom(99), 500)
		gs.LpUnbondings = []types.LpUnbonding{e}
		require.Error(t, gs.Validate())
	})

	// The worst one: entry.Shares.Amount.GT(total) dereferences a nil big.Int
	// and panics, so the node dies without saying why.
	t.Run("nil shares", func(t *testing.T) {
		gs := base()
		e := entry()
		e.Shares = sdk.Coin{Denom: types.LPShareDenom(1)}
		gs.LpUnbondings = []types.LpUnbonding{e}
		require.Error(t, gs.Validate())
	})

	t.Run("zero shares", func(t *testing.T) {
		gs := base()
		e := entry()
		e.Shares = sdk.NewInt64Coin(types.LPShareDenom(1), 0)
		gs.LpUnbondings = []types.LpUnbonding{e}
		require.Error(t, gs.Validate())
	})

	// Burns one pool's shares while paying out of another pool's reserves.
	// MsgRemoveLiquidity checks this; genesis import did not.
	t.Run("shares of the wrong pool", func(t *testing.T) {
		gs := base()
		e := entry()
		e.Shares = sdk.NewInt64Coin(types.LPShareDenom(2), 500)
		gs.LpUnbondings = []types.LpUnbonding{e}
		require.Error(t, gs.Validate())
	})

	// Completion time, pool and address are the store key, so a repeat would
	// restore only one of the two and silently drop the other.
	t.Run("duplicate key", func(t *testing.T) {
		gs := base()
		gs.LpUnbondings = []types.LpUnbonding{entry(), entry()}
		require.Error(t, gs.Validate())
	})
}
