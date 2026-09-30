package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// InitGenesis loads the pool. The tree's inner nodes are rebuilt from the
// leaves; the root history is loaded as exported, and the rebuilt root is
// recorded as the latest anchor if it is not already (a fresh chain records
// the empty root here). Turnstiles are checked against the balances the bank
// module has already loaded.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	if err := k.Params.Set(ctx, gs.Params); err != nil {
		return err
	}
	// Ensure the pool account exists, with its permissions, before anything
	// reads or pays it.
	k.authKeeper.GetModuleAccount(ctx, types.ModuleName)

	for _, a := range gs.Assets {
		id, err := k.RegisterAsset(ctx, a.Denom)
		if err != nil {
			return err
		}
		if !bytes.Equal(id, a.AssetId) {
			return fmt.Errorf("asset %q: id mismatch", a.Denom)
		}
	}

	t, err := k.tree(ctx)
	if err != nil {
		return err
	}
	for i, cm := range gs.Commitments {
		leaf, err := privacy.FieldFromBytes(cm)
		if err != nil {
			return fmt.Errorf("commitment %d: %w", i, err)
		}
		if _, err := t.Append(leaf); err != nil {
			return err
		}
	}
	if err := k.TreeSize.Set(ctx, t.Size()); err != nil {
		return err
	}

	for _, nf := range gs.Nullifiers {
		if err := k.Nullifiers.Set(ctx, nf); err != nil {
			return err
		}
	}

	// Oldest first, so the last record is the latest anchor.
	for i, r := range gs.Roots {
		if err := k.putRoot(ctx, r, i == len(gs.Roots)-1); err != nil {
			return err
		}
	}
	if err := k.recordRoot(ctx); err != nil {
		return err
	}

	for _, ts := range gs.Turnstiles {
		if err := k.Turnstiles.Set(ctx, ts.Denom, ts); err != nil {
			return err
		}
	}
	return k.AssertInvariants(ctx)
}

// ExportGenesis exports the pool.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error
	gs := types.GenesisState{}
	if gs.Params, err = k.Params.Get(ctx); err != nil {
		return nil, err
	}
	if err := k.Assets.Walk(ctx, nil, func(denom string, id []byte) (bool, error) {
		gs.Assets = append(gs.Assets, types.Asset{Denom: denom, AssetId: id})
		return false, nil
	}); err != nil {
		return nil, err
	}

	t, err := k.tree(ctx)
	if err != nil {
		return nil, err
	}
	gs.Commitments = make([][]byte, 0, t.Size())
	for i := uint64(0); i < t.Size(); i++ {
		l, err := t.Leaf(i)
		if err != nil {
			return nil, err
		}
		gs.Commitments = append(gs.Commitments, privacy.FieldBytes(l))
	}

	if err := k.Nullifiers.Walk(ctx, nil, func(nf []byte) (bool, error) {
		gs.Nullifiers = append(gs.Nullifiers, nf)
		return false, nil
	}); err != nil {
		return nil, err
	}

	latest, err := k.LatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	var latestRec *types.RootRecord
	if err := k.RootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		rec, err := k.Roots.Get(ctx, key.K2())
		if err != nil {
			return true, err
		}
		if bytes.Equal(rec.Root, latest) {
			latestRec = &rec
		} else {
			gs.Roots = append(gs.Roots, rec)
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	if latestRec != nil {
		gs.Roots = append(gs.Roots, *latestRec)
	}

	if err := k.Turnstiles.Walk(ctx, nil, func(_ string, ts types.Turnstile) (bool, error) {
		gs.Turnstiles = append(gs.Turnstiles, ts)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return &gs, nil
}
