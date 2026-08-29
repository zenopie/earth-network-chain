package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// A genesis round-trip must not blind the solvency check.
//
// SummedAccrued is derived state: the options come back from an export carrying
// real Accumulated balances and the module account comes back holding the coins
// behind them, but InitGenesis restores the options verbatim rather than through
// setOption, so nothing rebuilds the running sum unless InitGenesis does it.
//
// Left at zero the failure is silent in the worst way. owed = accrued + residue
// collapses to roughly residue, held stays where it was, and because
// SolvencyReport.Broken() tests only Short the whole gap reads as a tolerated
// surplus. Nothing errors. The check simply stops being able to see a shortfall
// up to the size of the balances it failed to import — and this matters now
// rather than hypothetically, because earth-1 relaunched from a fresh genesis
// and upgrades round-trip through ExportGenesis/InitGenesis.
func TestSummedAccruedSurvivesAGenesisRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	id := addDeadOption(t, e, "carries a balance across the export")

	_, voter := e.addr("voter")
	e.staking.bonded[voter] = math.NewInt(1_000_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.NoError(t, err)
	e.advance(time.Hour)

	// Move the vote away, which settles the option: the balance stops being a
	// pending claim against the index and becomes an Accumulated the export
	// carries.
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: types.LPRewardsOptionID, Percent: 100}},
	})
	require.NoError(t, err)

	before, err := e.k.GetSummedAccrued(e.ctx)
	require.NoError(t, err)
	require.True(t, before.IsPositive(), "nothing is being tested unless something accrued")

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	// The walk over the exported file, independent of anything the keeper kept.
	walked := math.ZeroInt()
	for _, st := range exported.Streams {
		for _, opt := range st.Options {
			walked = walked.Add(accruedOf(opt))
		}
	}
	require.Equal(t, before.String(), walked.String(),
		"the export must carry the balances the running sum claims")

	fresh := newTestEnv(t)
	// The coins move with the options. On a real chain this is the bank genesis
	// carrying the module account's balance; here the stub has to be told.
	fresh.bank.modBal = e.bank.modBal

	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	after, err := fresh.k.GetSummedAccrued(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, before.String(), after.String(),
		"summed_accrued must be rebuilt from the imported options, not left at zero")

	// The whole point: the check the EndBlocker runs still sees what it is for.
	require.NoError(t, fresh.k.AssertHotInvariants(fresh.ctx))
	require.NoError(t, fresh.k.AssertInvariants(fresh.ctx))
}

// Residue is the one figure no option carries, so it needs a genesis field.
//
// Dropping it strands the coins twice: SweepResidue has nothing to move, so
// emission earmarked for the community pool sits on the module account forever,
// and CheckSolvency understates what the module owes by the same amount.
func TestResidueSurvivesAGenesisRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	const dust = 4_321
	require.NoError(t, e.k.Residue.Set(e.ctx, math.NewInt(dust)))
	// The coins behind it are real and sitting on the module account.
	e.bank.modBal = e.bank.modBal.Add(sdk.NewInt64Coin("uerth", dust))

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(dust).String(), exported.Residue.String(),
		"residue is not derivable from the options, so it has to be exported")
	require.NoError(t, exported.Validate())

	fresh := newTestEnv(t)
	fresh.bank.modBal = e.bank.modBal
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	got, err := fresh.k.GetResidue(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(dust).String(), got.String())

	rep, err := fresh.k.CheckSolvency(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(dust).String(), rep.Residue.String(),
		"solvency must count the imported residue as owed")
}

// A negative residue would understate what the module owes, so it is refused at
// the file rather than absorbed at import.
func TestNegativeResidueIsRefused(t *testing.T) {
	gs := types.DefaultGenesis()
	gs.Residue = math.NewInt(-1)
	require.Error(t, gs.Validate())
}
