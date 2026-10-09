package app

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/stretchr/testify/require"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// The v1.2.1 handler on state shaped as v1.1.0 left it: records under the
// Groundworks prefix (positions then; note votes decode the same) and the
// totals and voters they fed. It installs the stake key, deletes every
// record with its totals and lease, re-files the validator's voter empty,
// takes the records' derth out of the supply (its backing stays), and the
// chain goes on with its invariants holding.
func TestUpgradeV1_2_1(t *testing.T) {
	g := initGwEnv(t)
	a := g.castGw(g.v, 1_000*gwE, g.split(60, 40))
	b := g.castGw(g.v, 500*gwE, g.split(0, 0, 100))
	require.NotEmpty(t, g.voter(g.v))
	// A proposal in voting across the upgrade keeps its snapshot's supply.
	pid := g.submitProposal()
	snapSupply, err := g.app.ShieldedStakingKeeper.SnapshotSupply(g.ctx(), pid, g.valoper(g.v))
	require.NoError(t, err)
	before := g.state(g.v).DerthSupply
	require.Equal(t, before, snapSupply)

	ctx := g.ctx()
	_, err = v1_2_1Handler(g.app)(ctx, upgradetypes.Plan{Name: UpgradeV1_2_1}, g.app.ModuleManager.GetVersionMap())
	require.NoError(t, err)

	params, err := g.app.ShieldedKeeper.Params.Get(g.ctx())
	require.NoError(t, err)
	want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v1_2_1StakeVK))
	require.NoError(t, err)
	require.Equal(t, want, params.VerifyingKeys[shieldedtypes.CircuitStake])

	require.False(t, g.hasVote(a.id))
	require.False(t, g.hasVote(b.id))
	require.Empty(t, g.voter(g.v))
	for _, o := range g.opts {
		require.True(t, g.allocated(o).IsZero(), "option %d", o)
	}
	require.Equal(t, before.Sub(math.NewIntFromUint64(1_500*gwE)), g.state(g.v).DerthSupply)
	after, err := g.app.ShieldedStakingKeeper.SnapshotSupply(g.ctx(), pid, g.valoper(g.v))
	require.NoError(t, err)
	require.Equal(t, snapSupply, after)

	// The chain goes on: blocks, an epoch, a new vote.
	g.next(5 * time.Second)
	g.invariants()
	g.days(1)
	g.castGw(g.v, 700*gwE, g.split(100))
	require.NotEmpty(t, g.voter(g.v))
	g.invariants()
}
