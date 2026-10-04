package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Audit 6 C-L4: when the last end-of-block recording of the stake roots
// failed, a proposal entering voting takes no roots (no note votes; a stale
// nf root would let a note spent into a position since vote twice), and the
// next successful recording clears the condition.
func TestAudit6SnapshotSkipsStaleRoots(t *testing.T) {
	e := initStakeEnv(t)
	k := e.app.ShieldedStakingKeeper

	fresh := e.submitProposal()
	snap, err := k.Snapshots.Get(e.ctx(), fresh)
	require.NoError(t, err)
	require.NotEmpty(t, snap.NfRoot, "a healthy snapshot has its roots")

	require.NoError(t, k.RootsStale.Set(e.ctx(), true)) // as a failed recording leaves it
	stale := e.submitProposal()
	snap, err = k.Snapshots.Get(e.ctx(), stale)
	require.NoError(t, err)
	require.Empty(t, snap.Root)
	require.Empty(t, snap.NfRoot)
	has, err := k.RootsStale.Has(e.ctx())
	require.NoError(t, err)
	require.False(t, has, "the block's own recording succeeded and cleared it")

	again := e.submitProposal()
	snap, err = k.Snapshots.Get(e.ctx(), again)
	require.NoError(t, err)
	require.NotEmpty(t, snap.NfRoot)
}
