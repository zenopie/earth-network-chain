package keeper_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
)

func TestQueries(t *testing.T) {
	f := initFixture(t)
	q := keeper.NewQueryServerImpl(f.k)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	msg0 := f.scenarioMsg(s, shieldedtest.Send2)
	_, err := f.runPrivate(msg0)
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)

	params, err := q.Params(f.ctx, &types.QueryParamsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, params.Params.VerifyingKeys[types.CircuitAction])

	tree, err := q.Tree(f.ctx, &types.QueryTreeRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(13), tree.TreeSize)
	require.Equal(t, tree.Root, hex.EncodeToString(tree.Anchor.Root), "the tree has not moved since the last anchor")
	require.Equal(t, uint64(13), tree.Anchor.TreeSize)

	roots, err := q.Roots(f.ctx, &types.QueryRootsRequest{})
	require.NoError(t, err)
	require.Len(t, roots.Roots, 3)
	require.Equal(t, tree.Anchor, roots.Roots[0], "newest first")

	root, err := q.Root(f.ctx, &types.QueryRootRequest{Root: hex.EncodeToString(msg0.Bundle.Actions[0].Anchor)})
	require.NoError(t, err)
	require.True(t, root.Valid)
	require.Positive(t, root.ExpiresAt)
	latest, err := q.Root(f.ctx, &types.QueryRootRequest{Root: tree.Root})
	require.NoError(t, err)
	require.True(t, latest.Valid)
	require.Zero(t, latest.ExpiresAt, "the latest root does not expire")
	_, err = q.Root(f.ctx, &types.QueryRootRequest{Root: "zz"})
	require.Error(t, err)

	nf, err := q.Nullifier(f.ctx, &types.QueryNullifierRequest{Nullifier: hex.EncodeToString(msg0.Bundle.Actions[1].Nullifier)})
	require.NoError(t, err)
	require.True(t, nf.Spent)
	other := f.scenarioMsg(s, shieldedtest.Multi3)
	nf, err = q.Nullifier(f.ctx, &types.QueryNullifierRequest{Nullifier: hex.EncodeToString(other.Bundle.Actions[0].Nullifier)})
	require.NoError(t, err)
	require.False(t, nf.Spent)

	assets, err := q.Assets(f.ctx, &types.QueryAssetsRequest{})
	require.NoError(t, err)
	require.ElementsMatch(t, types.DefaultAssets(), assets.Assets)

	ts, err := q.Turnstiles(f.ctx, &types.QueryTurnstilesRequest{})
	require.NoError(t, err)
	require.Len(t, ts.Turnstiles, 2)
	require.Equal(t, types.AnmlDenom, ts.Turnstiles[0].Denom)
	require.Equal(t, int64(1_116_000), ts.Turnstiles[1].In.Int64())
	require.Equal(t, int64(30_000), ts.Turnstiles[1].Out.Int64())
}
