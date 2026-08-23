package keeper

import (
	"fmt"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// The Options route is free to call and the option table is permissionless, so
// what one request returns must not grow with what strangers have paid to add.
func TestOptionsQueryIsPaged(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	const added = 250
	for i := 0; i < added; i++ {
		addDeadOption(t, e, fmt.Sprintf("option %d", i))
	}
	total := added + 2 // the two seeded integrated options

	q := NewQueryServerImpl(e.k)

	// A caller asking for more than the ceiling gets the ceiling, not the table.
	resp, err := q.Options(e.ctx, &types.QueryOptionsRequest{
		Stream:     types.STREAM_ID_GROUNDWORKS,
		Pagination: &query.PageRequest{Limit: 10_000},
	})
	require.NoError(t, err)
	require.Len(t, resp.Options, types.MaxOptionsPageSize)

	// Paging through reaches all of them, in id order, without repeats.
	seen := map[uint64]bool{}
	var next []byte
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20, "paging did not terminate")
		page, err := q.Options(e.ctx, &types.QueryOptionsRequest{
			Stream:     types.STREAM_ID_GROUNDWORKS,
			Pagination: &query.PageRequest{Key: next, Limit: 40},
		})
		require.NoError(t, err)
		for _, opt := range page.Options {
			require.False(t, seen[opt.Id], "option %d returned twice", opt.Id)
			require.Equal(t, types.STREAM_ID_GROUNDWORKS, opt.Stream,
				"a page leaked an option from the other stream")
			seen[opt.Id] = true
		}
		// The stream aggregates describe the stream, not the page.
		require.False(t, page.TotalWeight.IsNil())
		if page.Pagination == nil || len(page.Pagination.NextKey) == 0 {
			break
		}
		next = page.Pagination.NextKey
	}
	require.Len(t, seen, total)

	// The other stream is not reachable through this one's pages.
	other, err := q.Options(e.ctx, &types.QueryOptionsRequest{Stream: types.STREAM_ID_CARETAKER})
	require.NoError(t, err)
	require.Len(t, other.Options, 1, "the caretaker stream has only its seeded option")
}
