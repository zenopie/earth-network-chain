package keeper_test

import (
	"testing"

	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 4 C3: a genesis identity root that matches no tree state, dated far
// in the future, used to be accepted and stay a valid membership anchor for
// years. InitGenesis now refuses it; a record that is the rebuilt tree's
// root at its size, dated no later than genesis, is accepted.
func TestAudit4ForgedFutureIdentityRootRefused(t *testing.T) {
	reg := genesisReg(0, 1_700_000_000)
	leaf, err := keeper.IdentityLeaf(reg.Idc, reg.DscKey, reg.Country, reg.ActivatedAt, reg.PredecessorAt)
	require.NoError(t, err)
	real := realRootOf(t, leaf)
	forged := privacy.FieldBytes(privacy.U64(0xdeadbeef))

	at := func(f *fixture) sdk.Context {
		return sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(1_800_000_000, 0))
	}
	gsWith := func(roots ...types.IdentityRoot) types.GenesisState {
		return types.GenesisState{
			Params: types.DefaultParams(), IdentityTreeSize: 1,
			Registrations: []types.Registration{reg}, IdentityRoots: roots,
		}
	}

	// The PoC's record: forged root, a size past the tree, a future time.
	gs := gsWith(types.IdentityRoot{Root: forged, Height: 1, Time: 4_000_000_000, TreeSize: 999})
	require.Error(t, gs.Validate(), "tree_size past the tree")
	f := initFixture(t)
	require.Error(t, f.keeper.InitGenesis(at(f), gs))

	// Forged root at a plausible size and time.
	gs = gsWith(types.IdentityRoot{Root: forged, Height: 1, Time: 1_700_000_000, TreeSize: 1})
	require.NoError(t, gs.Validate())
	f = initFixture(t)
	require.ErrorContains(t, f.keeper.InitGenesis(at(f), gs), "not the root of the rebuilt tree")

	// The real root, dated after genesis.
	gs = gsWith(types.IdentityRoot{Root: real, Height: 1, Time: 1_900_000_000, TreeSize: 1})
	f = initFixture(t)
	require.ErrorContains(t, f.keeper.InitGenesis(at(f), gs), "after genesis time")

	// The real root: accepted, and an anchor.
	gs = gsWith(types.IdentityRoot{Root: real, Height: 1, Time: 1_700_000_000, TreeSize: 1})
	f = initFixture(t)
	ctx := at(f)
	require.NoError(t, f.keeper.InitGenesis(ctx, gs))
	require.NoError(t, f.keeper.CheckIdentityAnchor(ctx, real))
	require.Error(t, f.keeper.CheckIdentityAnchor(ctx, forged))
}

func realRootOf(t *testing.T, leaves ...fr.Element) []byte {
	t.Helper()
	m := merkle.NewMem()
	for _, l := range leaves {
		_, err := m.Append(l)
		require.NoError(t, err)
	}
	r, err := m.Root()
	require.NoError(t, err)
	return privacy.FieldBytes(r)
}

// A root recorded before a leaf was zeroed is not the rebuilt tree's root at
// its size any more: export drops it (an anchor only), so the export still
// imports under the C3 check.
func TestAudit4IdentityRootsRoundTripAfterZeroing(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockTime(time.Unix(10_600, 0).UTC())
	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	gs := types.GenesisState{Params: params, IdentityTreeSize: 2,
		Registrations: []types.Registration{genesisReg(0, 10_000), genesisReg(1, 10_600)}}
	require.NoError(t, f.keeper.InitGenesis(sdkCtx, gs))
	before, err := f.keeper.CurrentIdentityRoot(sdkCtx)
	require.NoError(t, err)

	dead := sdkCtx.WithBlockTime(time.Unix(11_100, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(dead))
	require.NoError(t, f.keeper.EndBlocker(dead))
	after, err := f.keeper.CurrentIdentityRoot(dead)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	require.NoError(t, f.keeper.CheckIdentityAnchor(dead, before), "still inside its window")

	out, err := f.keeper.ExportGenesis(dead)
	require.NoError(t, err)
	require.NoError(t, out.Validate())
	for _, r := range out.IdentityRoots {
		require.NotEqual(t, before, r.Root, "the pre-zeroing root is not exported")
	}
	f2 := initFixture(t)
	ctx2 := sdk.UnwrapSDKContext(f2.ctx).WithBlockTime(time.Unix(11_200, 0).UTC())
	require.NoError(t, f2.keeper.InitGenesis(ctx2, *out))
	require.NoError(t, f2.keeper.CheckIdentityAnchor(ctx2, after))
}
