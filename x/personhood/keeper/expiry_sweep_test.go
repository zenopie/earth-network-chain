package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

func genesisReg(i int, registeredAt int64) types.Registration {
	return types.Registration{
		Nullifier: []byte{byte(i / 256), byte(i % 256), 'n'}, LeafIndex: uint64(i),
		RegisteredAt: registeredAt, ActivatedAt: registeredAt,
		DscKey: privacy.FieldBytes(privacy.U64(77)), Country: "NZ", Idc: privacy.FieldBytes(privacy.U64(uint64(i + 1))),
	}
}

// A lapsed registration is retired by the sweep: its record, indexes and
// tallies go, and its identity leaf is zeroed so it stops proving membership.
func TestExpirySweepZeroesTheLeaf(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)
	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	gs := types.GenesisState{Params: params, IdentityTreeSize: 2,
		Registrations: []types.Registration{genesisReg(0, 10_000), genesisReg(1, 10_600)}}
	require.NoError(t, gs.Validate())
	require.NoError(t, f.keeper.InitGenesis(sdkCtx.WithBlockTime(time.Unix(10_600, 0).UTC()), gs))

	live := sdkCtx.WithBlockTime(time.Unix(10_600, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(live))
	l, err := f.keeper.IdentityLeafAt(live, 0)
	require.NoError(t, err)
	require.False(t, l.IsZero(), "a valid registration survives the sweep")

	dead := sdkCtx.WithBlockTime(time.Unix(11_100, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(dead))
	_, err = f.keeper.Registrations.Get(dead, genesisReg(0, 0).Nullifier)
	require.ErrorIs(t, err, collections.ErrNotFound)
	l, err = f.keeper.IdentityLeafAt(dead, 0)
	require.NoError(t, err)
	require.True(t, l.IsZero(), "the lapsed leaf is zeroed")
	l, err = f.keeper.IdentityLeafAt(dead, 1)
	require.NoError(t, err)
	require.False(t, l.IsZero(), "the other is not")
	count, err := f.keeper.RegCount.Get(dead)
	require.NoError(t, err)
	require.Equal(t, uint64(1), count)
	n, err := f.keeper.RegCountByCountry.Get(dead, "NZ")
	require.NoError(t, err)
	require.Equal(t, uint64(1), n)
}

// A mass expiry retires in bounded batches.
func TestExpirySweepIsBounded(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)
	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	const cohort = types.DefaultRegistrationSweepLimit + 25
	gs := types.GenesisState{Params: params, IdentityTreeSize: cohort}
	for i := 0; i < cohort; i++ {
		gs.Registrations = append(gs.Registrations, genesisReg(i, int64(10_000+i)))
	}
	require.NoError(t, f.keeper.InitGenesis(sdkCtx.WithBlockTime(time.Unix(10_000+cohort, 0).UTC()), gs))

	dead := sdkCtx.WithBlockTime(time.Unix(20_000, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(dead))
	count, err := f.keeper.RegCount.Get(dead)
	require.NoError(t, err)
	require.Equal(t, uint64(cohort-types.DefaultRegistrationSweepLimit), count)
	require.NoError(t, f.keeper.BeginBlocker(dead))
	count, err = f.keeper.RegCount.Get(dead)
	require.NoError(t, err)
	require.Zero(t, count)
}
