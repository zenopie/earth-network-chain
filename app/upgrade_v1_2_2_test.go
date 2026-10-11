package app

import (
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/stretchr/testify/require"
)

// The v1.2.2 handler runs on live-shaped state (a validator with note votes)
// and the chain goes on with its invariants holding.
func TestUpgradeV1_2_2(t *testing.T) {
	g := initGwEnv(t)
	g.castGw(g.v, 1_000*gwE, g.split(60, 40))

	_, err := v1_2_2Handler(g.app)(g.ctx(), upgradetypes.Plan{Name: UpgradeV1_2_2}, g.app.ModuleManager.GetVersionMap())
	require.NoError(t, err)
	require.NoError(t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
	g.invariants()
}
