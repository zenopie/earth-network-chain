package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// The identity tree: depth 32, one leaf per registration ever written,
// leaf = H(TAG_LEAF, idc, dsc_key, country, activated_at, predecessor_at)
// (zk/privacy.IdentityLeaf), computed here from
// public msg fields and the DSC's recorded issuing country. A leaf is zeroed when its registration expires, its Document
// Signer is revoked, or its holder switches to a new identity secret; a zero
// leaf proves nothing (see zk/merkle).
//
// A root becomes a membership anchor at the end of the block that produced it
// and stays one for identity_root_window_seconds after that block (the latest
// root always). That window is also how long a zeroed leaf can still prove
// against a root from before it was zeroed, which is why every activation
// bound subtracts it.

const (
	EventTypeIdentityLeaf = "identity_leaf"
	EventTypeIdentityRoot = "identity_root"
)

type identityStore struct {
	ctx context.Context
	k   Keeper
}

var _ merkle.NodeStore = identityStore{}

func (s identityStore) Get(level int, index uint64) (fr.Element, bool, error) {
	bz, err := s.k.IdentityNodes.Get(s.ctx, collections.Join(uint32(level), index))
	if errors.Is(err, collections.ErrNotFound) {
		return fr.Element{}, false, nil
	} else if err != nil {
		return fr.Element{}, false, err
	}
	e, err := privacy.FieldFromBytes(bz)
	return e, err == nil, err
}

func (s identityStore) Set(level int, index uint64, v fr.Element) error {
	return s.k.IdentityNodes.Set(s.ctx, collections.Join(uint32(level), index), privacy.FieldBytes(v))
}

func (k Keeper) identityTree(ctx context.Context) (*merkle.Tree, error) {
	size, err := k.IdentitySize.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		size = 0
	} else if err != nil {
		return nil, err
	}
	return merkle.New(identityStore{ctx: ctx, k: k}, size), nil
}

// IdentityLeaf computes the leaf a registration writes. country is the
// registration's recorded issuing country (see privacy.CountryField: "" and
// anything not an alpha-2 code are 0, unknown).
func IdentityLeaf(idc, dscKey []byte, country string, activatedAt, predecessorAt int64) (fr.Element, error) {
	idcEl, err := types.Field("idc", idc)
	if err != nil {
		return fr.Element{}, err
	}
	dsc, err := types.OptionalField("dsc_key", dscKey)
	if err != nil {
		return fr.Element{}, err
	}
	if predecessorAt < 0 {
		predecessorAt = 0
	}
	return privacy.IdentityLeaf(idcEl, dsc, privacy.CountryField(country), uint64(activatedAt), uint64(predecessorAt)), nil
}

// appendLeaf writes a new identity leaf and returns its index.
func (k Keeper) appendLeaf(ctx context.Context, leaf fr.Element) (uint64, error) {
	t, err := k.identityTree(ctx)
	if err != nil {
		return 0, err
	}
	i, err := t.Append(leaf)
	if errors.Is(err, merkle.ErrFull) {
		return 0, types.ErrIdentityTreeFull
	} else if err != nil {
		return 0, err
	}
	if err := k.IdentitySize.Set(ctx, t.Size()); err != nil {
		return 0, err
	}
	emitLeaf(ctx, i, leaf)
	return i, nil
}

// SuccessionLeaf is the identity tree's succession leaf for a passport whose
// last registration was to idcOld registering to idcNew.
func SuccessionLeaf(idcOld, idcNew []byte) (fr.Element, error) {
	a, err := privacy.FieldFromBytes(idcOld)
	if err != nil {
		return fr.Element{}, err
	}
	b, err := privacy.FieldFromBytes(idcNew)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.SuccessionLeaf(a, b), nil
}

// appendSuccession appends the succession leaf (idcOld, idcNew) and records
// it for export. It is never zeroed: a move along it also needs the
// successor's own leaf, which a switch or expiry zeroes.
func (k Keeper) appendSuccession(ctx context.Context, idcOld, idcNew []byte) error {
	leaf, err := SuccessionLeaf(idcOld, idcNew)
	if err != nil {
		return err
	}
	index, err := k.appendLeaf(ctx, leaf)
	if err != nil {
		return err
	}
	return k.Successions.Set(ctx, index, types.Succession{LeafIndex: index, IdcOld: idcOld, IdcNew: idcNew})
}

// zeroLeaf empties a leaf: the registration behind it no longer proves
// membership once the roots from before this block have aged out.
func (k Keeper) zeroLeaf(ctx context.Context, index uint64) error {
	t, err := k.identityTree(ctx)
	if err != nil {
		return err
	}
	if err := t.Update(index, fr.Element{}); err != nil {
		return err
	}
	emitLeaf(ctx, index, fr.Element{})
	return nil
}

func emitLeaf(ctx context.Context, index uint64, leaf fr.Element) {
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(EventTypeIdentityLeaf,
		sdk.NewAttribute("index", strconv.FormatUint(index, 10)),
		sdk.NewAttribute("leaf", hex.EncodeToString(privacy.FieldBytes(leaf))),
	))
}

// IdentityLeafAt returns the leaf at index (zero if empty or zeroed).
func (k Keeper) IdentityLeafAt(ctx context.Context, index uint64) (fr.Element, error) {
	t, err := k.identityTree(ctx)
	if err != nil {
		return fr.Element{}, err
	}
	return t.Leaf(index)
}

// IdentityTreeSize is the number of leaves ever written.
func (k Keeper) IdentityTreeSize(ctx context.Context) (uint64, error) {
	t, err := k.identityTree(ctx)
	if err != nil {
		return 0, err
	}
	return t.Size(), nil
}

// CurrentIdentityRoot is the root including this block's writes. It becomes
// an anchor only once EndBlock records it.
func (k Keeper) CurrentIdentityRoot(ctx context.Context) ([]byte, error) {
	t, err := k.identityTree(ctx)
	if err != nil {
		return nil, err
	}
	r, err := t.Root()
	if err != nil {
		return nil, err
	}
	return privacy.FieldBytes(r), nil
}

// recordIdentityRoot records the current root if the tree moved this block.
func (k Keeper) recordIdentityRoot(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	root, err := k.CurrentIdentityRoot(ctx)
	if err != nil {
		return err
	}
	latest, err := k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if bytes.Equal(latest, root) {
		return nil
	}
	size, err := k.IdentityTreeSize(ctx)
	if err != nil {
		return err
	}
	return k.putIdentityRoot(ctx, types.IdentityRoot{
		Root: root, Height: sdkCtx.BlockHeight(), Time: sdkCtx.BlockTime().Unix(), TreeSize: size,
	}, true)
}

func (k Keeper) putIdentityRoot(ctx context.Context, rec types.IdentityRoot, latest bool) error {
	// A root can recur: the tree's root does not commit to its size, so when
	// every leaf appended since root R was zeroed again, the root is R once
	// more. Its record moves to the new time; its old by-time entry must go
	// with it, or the prune would later delete the record through the stale
	// entry while the new one still holds it (the anchor expiring early), and
	// export would find a by-time entry with no record (audit 6 B6-2).
	if old, err := k.IdentityRoots.Get(ctx, rec.Root); err == nil {
		if old.Time != rec.Time {
			if err := k.IdentityRootsByTime.Remove(ctx, collections.Join(old.Time, rec.Root)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.IdentityRoots.Set(ctx, rec.Root, rec); err != nil {
		return err
	}
	if err := k.IdentityRootsByTime.Set(ctx, collections.Join(rec.Time, rec.Root)); err != nil {
		return err
	}
	if !latest {
		return nil
	}
	if err := k.LatestIdentityRoot.Set(ctx, rec.Root); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(EventTypeIdentityRoot,
		sdk.NewAttribute("root", hex.EncodeToString(rec.Root)),
		sdk.NewAttribute("tree_size", strconv.FormatUint(rec.TreeSize, 10)),
		sdk.NewAttribute("height", strconv.FormatInt(rec.Height, 10)),
	))
	return nil
}

// pruneIdentityRoots deletes up to limit records past the window, oldest
// first, never the latest.
func (k Keeper) pruneIdentityRoots(ctx context.Context, window int64, limit int) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	latest, err := k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	var doomed []collections.Pair[int64, []byte]
	err = k.IdentityRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if len(doomed) >= limit || key.K1()+window >= now {
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
		if err := k.IdentityRootsByTime.Remove(ctx, key); err != nil {
			return err
		}
		if err := k.IdentityRoots.Remove(ctx, key.K2()); err != nil {
			return err
		}
	}
	return nil
}

// CheckIdentityAnchor refuses a root that is not a valid membership anchor now.
func (k Keeper) CheckIdentityAnchor(ctx context.Context, root []byte) error {
	rec, err := k.IdentityRoots.Get(ctx, root)
	if errors.Is(err, collections.ErrNotFound) {
		return types.ErrUnknownIdentityRoot.Wrapf("%X", root)
	} else if err != nil {
		return err
	}
	latest, err := k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if bytes.Equal(latest, root) {
		return nil
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() > rec.Time+params.IdentityRootWindowSecondsOrDefault() {
		return types.ErrUnknownIdentityRoot.Wrapf("%X expired", root)
	}
	return nil
}

// IdentityRootWindow is how long a superseded identity root stays an anchor.
func (k Keeper) IdentityRootWindow(ctx context.Context) (int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	return params.IdentityRootWindowSecondsOrDefault(), nil
}
