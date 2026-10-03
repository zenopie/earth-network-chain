package keeper_test

import (
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// AUDIT3 F4: genesis root records used to be taken on faith (a forged root,
// dated far in the future, stayed a valid anchor for years). InitGenesis now
// checks every record against the rebuilt tree at its tree_size and refuses
// one dated after genesis.
func TestAudit3GenesisForgedRootRefused(t *testing.T) {
	f := initFixtureEmpty(t, newFakeBank())
	var forged fr.Element
	forged.SetUint64(0xf0f0) // stands for the root of a tree only the attacker knows
	gs := types.DefaultGenesis()
	gs.Params.VerifyingKeys = map[string][]byte{types.CircuitAction: f.prover.VerifyingKey(t)}
	gs.Roots = []types.RootRecord{{Root: privacy.FieldBytes(forged), Height: 1, Time: f.ctx.BlockTime().Unix(), TreeSize: 0}}
	require.NoError(t, gs.Validate())
	require.ErrorContains(t, f.k.InitGenesis(f.ctx, *gs), "is not the root of the first 0 commitments")
}

func TestAudit3GenesisRootRecordsVerified(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.DoubleSpend)
	gs, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, gs.Roots, 6)

	fresh := func() *fixture {
		g := initFixtureEmpty(t, f.bank)
		g.ctx = g.ctx.WithBlockTime(f.ctx.BlockTime()).WithBlockHeight(f.ctx.BlockHeight())
		return g
	}
	// The honest export imports.
	g := fresh()
	require.NoError(t, g.k.InitGenesis(g.ctx, *gs))

	// A real root claimed at the wrong tree size is refused.
	bad := *gs
	bad.Roots = append([]types.RootRecord(nil), gs.Roots...)
	bad.Roots[1].TreeSize++
	g = fresh()
	require.ErrorContains(t, g.k.InitGenesis(g.ctx, bad), "is not the root of the first")

	// A real root dated after genesis is refused.
	bad.Roots = append([]types.RootRecord(nil), gs.Roots...)
	bad.Roots[1].Time = f.ctx.BlockTime().Add(time.Second).Unix()
	g = fresh()
	require.ErrorContains(t, g.k.InitGenesis(g.ctx, bad), "after genesis time")
}
