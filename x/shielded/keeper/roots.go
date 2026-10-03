package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// A root becomes an anchor at the end of the block that produced it and stays
// one for root_window_seconds from that block's time. The latest recorded
// root never expires: a chain that went quiet for longer than the window must
// not strand every note.
//
// Anchors are recorded per block rather than per append. A proof can only
// target a root that existed at the end of some block, which is also the only
// state a wallet ever syncs, and it bounds the history to one entry a block.

// recordRoot records the current root if the tree moved this block.
func (k Keeper) recordRoot(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	t, err := k.tree(ctx)
	if err != nil {
		return err
	}
	r, err := t.Root()
	if err != nil {
		return err
	}
	root := r.Bytes()
	latest, err := k.LatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if bytes.Equal(latest, root[:]) {
		return nil
	}
	return k.putRoot(ctx, types.RootRecord{
		Root: root[:], Height: sdkCtx.BlockHeight(), Time: sdkCtx.BlockTime().Unix(), TreeSize: t.Size(),
	}, true)
}

// putRoot stores a record (and, when latest, makes it the newest anchor).
func (k Keeper) putRoot(ctx context.Context, rec types.RootRecord, latest bool) error {
	if err := k.Roots.Set(ctx, rec.Root, rec); err != nil {
		return err
	}
	if err := k.RootsByTime.Set(ctx, collections.Join(rec.Time, rec.Root)); err != nil {
		return err
	}
	if latest {
		if err := k.LatestRoot.Set(ctx, rec.Root); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeRoot,
			sdk.NewAttribute(types.AttributeKeyRoot, hex.EncodeToString(rec.Root)),
			sdk.NewAttribute(types.AttributeKeyTreeSize, strconv.FormatUint(rec.TreeSize, 10)),
			sdk.NewAttribute(types.AttributeKeyHeight, strconv.FormatInt(rec.Height, 10)),
		))
	}
	return nil
}

// pruneRoots deletes up to limit records older than the window, oldest first,
// never the latest.
func (k Keeper) pruneRoots(ctx context.Context, window uint64, limit int) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	latest, err := k.LatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	var doomed []collections.Pair[int64, []byte]
	err = k.RootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if len(doomed) >= limit || key.K1()+int64(window) >= now {
			return true, nil
		}
		if !bytes.Equal(key.K2(), latest) {
			doomed = append(doomed, key)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	for _, key := range doomed {
		if err := k.RootsByTime.Remove(ctx, key); err != nil {
			return err
		}
		if err := k.Roots.Remove(ctx, key.K2()); err != nil {
			return err
		}
	}
	return nil
}

// Anchor reports whether root is a valid anchor now, with its record and the
// unix second it expires at (0 for the latest root).
func (k Keeper) Anchor(ctx context.Context, root []byte) (valid bool, rec types.RootRecord, expiresAt int64, err error) {
	rec, err = k.Roots.Get(ctx, root)
	if errors.Is(err, collections.ErrNotFound) {
		return false, rec, 0, nil
	} else if err != nil {
		return false, rec, 0, err
	}
	latest, err := k.LatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return false, rec, 0, err
	}
	if bytes.Equal(latest, root) {
		return true, rec, 0, nil
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return false, rec, 0, err
	}
	expiresAt = rec.Time + int64(params.RootWindowSeconds)
	return sdk.UnwrapSDKContext(ctx).BlockTime().Unix() <= expiresAt, rec, expiresAt, nil
}

// AnchorCheckTxMarginSeconds: in CheckTx and ReCheckTx an anchor must stay
// valid at least this long past the last committed block (audit 5 L-SH1). A
// tx whose anchor lapses before it lands would pass CheckTx, be proposed and
// then fail its ante in the block, paying nothing while taking block gas and
// the private-action cap; with the margin it leaves the mempool first.
// Wallets pick an anchor with more than this left (the window is two weeks).
const AnchorCheckTxMarginSeconds = 120

// checkAnchor refuses a root that is not a valid anchor (in CheckTx and
// ReCheckTx: one lapsing within AnchorCheckTxMarginSeconds).
func (k Keeper) checkAnchor(ctx context.Context, root []byte) error {
	ok, _, expiresAt, err := k.Anchor(ctx, root)
	if err != nil {
		return err
	}
	if !ok {
		return types.ErrUnknownRoot.Wrapf("%X", root)
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if (sdkCtx.IsCheckTx() || sdkCtx.IsReCheckTx()) && expiresAt != 0 &&
		expiresAt < sdkCtx.BlockTime().Unix()+AnchorCheckTxMarginSeconds {
		return types.ErrUnknownRoot.Wrapf("%X expires at %d, within %ds of the last block: pick a newer anchor",
			root, expiresAt, AnchorCheckTxMarginSeconds)
	}
	return nil
}

// AnchorsValidAt reports whether every action anchor of msg is a valid
// anchor at time t (unix seconds), as a block at t would see it. For
// PrepareProposal, which leaves out a private tx certain to fail on a lapsed
// anchor.
func (k Keeper) AnchorsValidAt(ctx sdk.Context, msg types.PrivateMsg, t int64) bool {
	at := ctx.WithBlockTime(time.Unix(t, 0))
	seen := map[string]bool{}
	for _, b := range msg.PrivateBundles() {
		for j := range b.Actions {
			root := b.Actions[j].Anchor
			if seen[string(root)] {
				continue
			}
			ok, _, _, err := k.Anchor(at, root)
			if err != nil || !ok {
				return false
			}
			seen[string(root)] = true
		}
	}
	return true
}
