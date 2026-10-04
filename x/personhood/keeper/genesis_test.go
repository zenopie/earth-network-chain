package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{Params: types.DefaultParams()}
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, genesisState))
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.EqualExportedValues(t, genesisState.Params, got.Params)
}

// A populated genesis round-trips, and every index over the registrations —
// the identity tree included — is rebuilt rather than carried.
func TestGenesisRoundTripsPopulatedState(t *testing.T) {
	f := initFixture(t)
	r0, r2 := genesisReg(0, 1_700_000_000), genesisReg(2, 1_700_000_500)
	r0.Country, r2.Country = "GB", "US"
	original := types.GenesisState{
		Params:           types.DefaultParams(),
		IdentityTreeSize: 3, // leaf 1 was zeroed
		Registrations:    []types.Registration{r0, r2},
		ClaimNullifiers:  []types.ClaimNullifier{{Day: 20_000, Nullifier: privacy.FieldBytes(privacy.U64(5))}},
		CaretakerVotes:   []types.CaretakerVote{{Nullifier: privacy.FieldBytes(privacy.U64(6)), ExpiresAt: 1_700_100_000}},
	}
	require.NoError(t, original.Validate())
	f.ctx = sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_700_000_500, 0).UTC())
	require.NoError(t, f.keeper.InitGenesis(f.ctx, original))

	count, err := f.keeper.RegCount.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), count)
	n, err := f.keeper.RegCountByCountry.Get(f.ctx, "GB")
	require.NoError(t, err)
	require.Equal(t, uint64(1), n)
	size, err := f.keeper.IdentityTreeSize(f.ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), size)
	l, err := f.keeper.IdentityLeafAt(f.ctx, 1)
	require.NoError(t, err)
	require.True(t, l.IsZero())
	l, err = f.keeper.IdentityLeafAt(f.ctx, 2)
	require.NoError(t, err)
	require.Equal(t, privacy.IdentityLeaf(privacy.U64(3), privacy.U64(77), privacy.CountryField("US"), 1_700_000_500, 0), l)
	// The rebuilt root is the latest anchor.
	root, err := f.keeper.CurrentIdentityRoot(f.ctx)
	require.NoError(t, err)
	require.NoError(t, f.keeper.CheckIdentityAnchor(f.ctx, root))

	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, original.Registrations, got.Registrations)
	require.Equal(t, original.IdentityTreeSize, got.IdentityTreeSize)
	require.Equal(t, original.ClaimNullifiers, got.ClaimNullifiers)
	require.Equal(t, original.CaretakerVotes, got.CaretakerVotes)
	require.Len(t, got.IdentityRoots, 1)
}

func TestGenesisRejectsADuplicateNullifier(t *testing.T) {
	r0, r1 := genesisReg(0, 1), genesisReg(1, 2)
	r1.Nullifier = r0.Nullifier
	gs := types.GenesisState{Params: types.DefaultParams(), IdentityTreeSize: 2, Registrations: []types.Registration{r0, r1}}
	err := gs.Validate()
	require.ErrorContains(t, err, "one passport, two humans")
}

func TestGenesisRejectsASharedOrMissingLeaf(t *testing.T) {
	r0, r1 := genesisReg(0, 1), genesisReg(1, 2)
	r1.LeafIndex = 0
	gs := types.GenesisState{Params: types.DefaultParams(), IdentityTreeSize: 2, Registrations: []types.Registration{r0, r1}}
	require.ErrorContains(t, gs.Validate(), "two registrations")
	gs = types.GenesisState{Params: types.DefaultParams(), IdentityTreeSize: 1, Registrations: []types.Registration{genesisReg(3, 1)}}
	require.ErrorContains(t, gs.Validate(), "outside the identity tree")
}

// The buyback clock must NOT survive an export: the buyback mints for elapsed
// wall-clock time, and a restarted chain was not running during the gap.
func TestGenesisDoesNotCarryTheBuybackClock(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, types.GenesisState{
		Params:      types.DefaultParams(),
		LastBuyback: 1_700_000_000_000_000_000,
	}))
	stored, err := f.keeper.LastBuyback.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000_000_000), stored)
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Zero(t, got.LastBuyback)
}
