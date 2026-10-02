package keeper

import (
	"context"

	"github.com/earth-network/earth/x/shielded/types"
)

// EndBlocker records this block's root as an anchor if the tree moved, prunes
// expired anchors (at most types.RootPruneLimit), resets the private tx
// action counter and checks the turnstiles that moved. An error halts the chain,
// which is intended only for the last: the pool is already wrong by then.
func (k Keeper) EndBlocker(ctx context.Context) error {
	if err := k.recordRoot(ctx); err != nil {
		return err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if err := k.pruneRoots(ctx, params.RootWindowSeconds, types.RootPruneLimit); err != nil {
		return err
	}
	if err := k.PrivateActionCount.Remove(ctx); err != nil {
		return err
	}
	return k.assertMoved(ctx)
}
