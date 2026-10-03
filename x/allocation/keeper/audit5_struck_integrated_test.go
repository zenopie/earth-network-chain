package keeper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// Audit 5 A1: a struck INTEGRATED option survives an export -> import -> 30
// day prune without halting BeginBlock, and does not come back into the
// handler set.
func TestAudit5StruckIntegratedSurvivesExportImportPrune(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	const id = uint64(2)
	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.Equal(t, types.ALLOCATION_KIND_INTEGRATED, opt.Kind, "seeded option 2 is integrated")
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id))

	exported := mustExport(t, e)
	require.NoError(t, exported.Validate())

	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))
	has, err := fresh.k.IntegratedOptions.Has(fresh.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.False(t, has, "a struck option must not rejoin the handler set on import")

	for i := 0; i < 3; i++ {
		fresh.ctx = fresh.ctx.WithBlockTime(fresh.ctx.BlockTime().Add(time.Duration(types.OptionIdleGrace)*time.Second/2 + time.Hour))
		require.NoError(t, fresh.k.BeginBlocker(fresh.ctx))
	}
	_, err = fresh.k.Options.Get(fresh.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.Error(t, err, "the struck option is pruned")
}

// A handler-set entry with no option (state an older binary could write) is
// dropped, not a halt; pruneOption clears both.
func TestAudit5DanglingIntegratedEntryDropped(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	kk := optionKey(types.STREAM_ID_GROUNDWORKS, 2)
	require.NoError(t, e.k.Options.Remove(e.ctx, kk))
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	has, err := e.k.IntegratedOptions.Has(e.ctx, kk)
	require.NoError(t, err)
	require.False(t, has)
}
