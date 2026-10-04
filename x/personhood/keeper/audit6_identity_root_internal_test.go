package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
)

// Audit 6 B6-2: a root that recurs (every leaf appended since it was zeroed
// again) moves to its new time, and its old by-time entry goes with it: the
// prune keeps it for its full window, and export finds every by-time entry's
// record.
func TestAudit6RecurringIdentityRoot(t *testing.T) {
	k, ctx := regKeeper(t, nil)
	r0 := make([]byte, 32)
	r0[31] = 1
	r1 := make([]byte, 32)
	r1[31] = 2
	t0 := ctx.BlockTime().Unix()
	put := func(root []byte, at int64, size uint64) {
		require.NoError(t, k.putIdentityRoot(ctx, types.IdentityRoot{Root: root, Height: at - t0 + 1, Time: at, TreeSize: size}, true))
	}
	put(r0, t0, 0)
	put(r1, t0+10, 1)
	put(r0, t0+3000, 2) // the leaf appended at t0+10 was zeroed: r0 again

	n := 0
	require.NoError(t, k.IdentityRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		rec, err := k.IdentityRoots.Get(ctx, key.K2())
		require.NoError(t, err)
		require.Equal(t, rec.Time, key.K1(), "each by-time entry is its record's")
		n++
		return false, nil
	}))
	require.Equal(t, 2, n)

	// Past t0's window, before t0+3000's: r1 goes, r0 (recorded at t0+3000)
	// stays.
	window := int64(time.Hour / time.Second)
	at := ctx.WithBlockTime(time.Unix(t0+window+100, 0))
	put(r1, t0+window+50, 1) // r1 latest again, so r0 is not protected as latest
	require.NoError(t, k.pruneIdentityRoots(at, window, 100))
	_, err := k.IdentityRoots.Get(at, r0)
	require.NoError(t, err, "r0's record outlives its stale time")
	_, err = k.ExportGenesis(at)
	require.NoError(t, err)
}
