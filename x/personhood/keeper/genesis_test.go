package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"

	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params: types.DefaultParams(),
	}

	f := initFixture(t)
	err := f.keeper.InitGenesis(f.ctx, genesisState)
	require.NoError(t, err)
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.EqualExportedValues(t, genesisState.Params, got.Params)
}

// The test that would have caught the export dropping every registration.
//
// The old one round-tripped an empty genesis, which passes no matter what
// ExportGenesis does: there is nothing to lose. This one populates the state
// first, and asserts on the records and on every index derived from them.
func TestGenesisRoundTripsPopulatedState(t *testing.T) {
	f := initFixture(t)

	addr1, err := f.addressCodec.BytesToString(sdk.AccAddress("human-one___________"))
	require.NoError(t, err)
	addr2, err := f.addressCodec.BytesToString(sdk.AccAddress("human-two___________"))
	require.NoError(t, err)

	original := types.GenesisState{
		Params:      types.DefaultParams(),
		LastBuyback: 1_700_000_000_000_000_000,
		Registrations: []types.Registration{
			{
				Nullifier:     []byte{0xaa, 0x01},
				Address:       addr1,
				RegisteredAt:  1_700_000_000,
				LastAnmlClaim: 1_700_086_400,
				DscKey:        []byte{0xde, 0xad},
				Country:       "GBR",
			},
			{
				Nullifier:     []byte{0xaa, 0x02},
				Address:       addr2,
				RegisteredAt:  1_700_000_500,
				LastAnmlClaim: 0,
				DscKey:        []byte{0xde, 0xad},
				Country:       "USA",
			},
		},
	}
	require.NoError(t, original.Validate())
	require.NoError(t, f.keeper.InitGenesis(f.ctx, original))

	// Derived state has to be rebuilt, not carried — otherwise an export could
	// ship counters that disagree with the records they count.
	count, err := f.keeper.RegCount.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), count)

	n, err := f.keeper.RegCountByCountry.Get(f.ctx, "GBR")
	require.NoError(t, err)
	require.Equal(t, uint64(1), n)
	n, err = f.keeper.RegCountByDsc.Get(f.ctx, []byte{0xde, 0xad})
	require.NoError(t, err)
	require.Equal(t, uint64(2), n, "both humans share a Document Signer")

	addrBz, err := f.addressCodec.StringToBytes(addr1)
	require.NoError(t, err)
	nullifier, err := f.keeper.RegByAddr.Get(f.ctx, addrBz)
	require.NoError(t, err)
	require.Equal(t, []byte{0xaa, 0x01}, nullifier)

	// And the round trip returns what went in.
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, got.Registrations, 2)
	require.ElementsMatch(t, original.Registrations, got.Registrations)
}

// A nullifier registered twice is the anti-Sybil property already broken, so an
// import must refuse it rather than start a chain that has quietly lost it.
func TestGenesisRejectsADuplicateNullifier(t *testing.T) {
	f := initFixture(t)
	addr, err := f.addressCodec.BytesToString(sdk.AccAddress("human-one___________"))
	require.NoError(t, err)
	other, err := f.addressCodec.BytesToString(sdk.AccAddress("human-two___________"))
	require.NoError(t, err)

	gs := types.GenesisState{
		Params: types.DefaultParams(),
		Registrations: []types.Registration{
			{Nullifier: []byte{0x01}, Address: addr, RegisteredAt: 1, DscKey: []byte{0x1}, Country: "GBR"},
			{Nullifier: []byte{0x01}, Address: other, RegisteredAt: 2, DscKey: []byte{0x1}, Country: "GBR"},
		},
	}
	err = gs.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "one passport, two humans")
}

// The buyback clock must NOT survive an export, which is the opposite of every
// other field here and is the whole reason it is called out in the schema.
//
// The buyback mints for elapsed wall-clock time (now - last). A chain restarted
// from an export did not run during the gap, so carrying the timestamp across
// would mint the entire downtime in the first block — a week of downtime is
// 604,800 ERTH minted and swapped into the ANML pool at once. x/earth reaches the
// same conclusion by never carrying LastMintTime at all.
func TestGenesisDoesNotCarryTheBuybackClock(t *testing.T) {
	f := initFixture(t)

	require.NoError(t, f.keeper.InitGenesis(f.ctx, types.GenesisState{
		Params:      types.DefaultParams(),
		LastBuyback: 1_700_000_000_000_000_000,
	}))

	// It is honoured in state, so an import can still be handed one...
	stored, err := f.keeper.LastBuyback.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000_000_000), stored)

	// ...but an export never produces one.
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Zero(t, got.LastBuyback,
		"exporting the clock would mint the whole restart gap in one block")
}
