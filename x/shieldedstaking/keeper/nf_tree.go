package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// The stake nullifier tree. Every spent stake nullifier is inserted, in spend
// order, into an indexed (sorted) depth-32 Poseidon2 Merkle tree (zk/indexed),
// so a stake vote can prove its note's nullifier was NOT spent when voting
// began without revealing it (circuits/vote). Stored:
//
//	StakeNullifiers  value -> leaf index   (ordered by value: the low-leaf lookup)
//	StakeNfValues    leaf index -> value   (insertion order: export, wallets)
//	StakeNfNodes     the Merkle nodes; StakeNfSize the leaf count (sentinel included)
//	StakeNfLatestRoot/Size  recorded at the end of a block that changed it
//
// Leaf 0 is the sentinel (value 0), written by the first insert; a leaf's
// successor pointers are not stored, they are its value's successor in
// StakeNullifiers. An insert reads its neighbours and rewrites two paths.
// No root window: only proposal snapshots prove against this tree, and they
// keep their own root (ProposalSnapshot.nf_root).

type nfNodeStore struct {
	ctx context.Context
	k   Keeper
}

var _ merkle.NodeStore = nfNodeStore{}

func (s nfNodeStore) Get(level int, index uint64) (fr.Element, bool, error) {
	bz, err := s.k.StakeNfNodes.Get(s.ctx, collections.Join(uint32(level), index))
	if errors.Is(err, collections.ErrNotFound) {
		return fr.Element{}, false, nil
	} else if err != nil {
		return fr.Element{}, false, err
	}
	e, err := privacy.FieldFromBytes(bz)
	return e, err == nil, err
}

func (s nfNodeStore) Set(level int, index uint64, v fr.Element) error {
	return s.k.StakeNfNodes.Set(s.ctx, collections.Join(uint32(level), index), privacy.FieldBytes(v))
}

// nfIndex is indexed.Index over StakeNullifiers / StakeNfValues.
type nfIndex struct {
	ctx context.Context
	k   Keeper
}

var _ indexed.Index = nfIndex{}

func (x nfIndex) Get(v fr.Element) (uint64, bool, error) {
	i, err := x.k.StakeNullifiers.Get(x.ctx, privacy.FieldBytes(v))
	if errors.Is(err, collections.ErrNotFound) {
		return 0, false, nil
	}
	return i, err == nil, err
}

func (x nfIndex) first(r collections.Ranger[[]byte]) (fr.Element, uint64, bool, error) {
	it, err := x.k.StakeNullifiers.Iterate(x.ctx, r)
	if err != nil {
		return fr.Element{}, 0, false, err
	}
	defer it.Close()
	if !it.Valid() {
		return fr.Element{}, 0, false, nil
	}
	kv, err := it.KeyValue()
	if err != nil {
		return fr.Element{}, 0, false, err
	}
	v, err := privacy.FieldFromBytes(kv.Key)
	if err != nil {
		return fr.Element{}, 0, false, err
	}
	return v, kv.Value, true, nil
}

func (x nfIndex) Below(v fr.Element) (fr.Element, uint64, bool, error) {
	return x.first(new(collections.Range[[]byte]).EndExclusive(privacy.FieldBytes(v)).Descending())
}

func (x nfIndex) Above(v fr.Element) (fr.Element, uint64, bool, error) {
	return x.first(new(collections.Range[[]byte]).StartExclusive(privacy.FieldBytes(v)))
}

func (x nfIndex) Put(v fr.Element, index uint64) error {
	b := privacy.FieldBytes(v)
	if err := x.k.StakeNullifiers.Set(x.ctx, b, index); err != nil {
		return err
	}
	return x.k.StakeNfValues.Set(x.ctx, index, b)
}

func (k Keeper) nfTree(ctx context.Context) (*indexed.Tree, error) {
	size, err := k.StakeNfSize.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		size = 0
	} else if err != nil {
		return nil, err
	}
	return indexed.New(nfNodeStore{ctx: ctx, k: k}, nfIndex{ctx: ctx, k: k}, size), nil
}

// insertStakeNullifier inserts a spent stake nullifier, emitting it with its
// leaf index (the wallets' stream, in insertion order).
func (k Keeper) insertStakeNullifier(ctx context.Context, nf []byte) error {
	v, err := privacy.FieldFromBytes(nf)
	if err != nil {
		return types.ErrStakeTree.Wrapf("nullifier: %v", err)
	}
	t, err := k.nfTree(ctx)
	if err != nil {
		return err
	}
	idx, err := t.Insert(v)
	switch {
	case errors.Is(err, indexed.ErrExists):
		return types.ErrStakeNullifierSpent.Wrapf("%X", nf)
	case errors.Is(err, merkle.ErrFull):
		return types.ErrStakeTree.Wrap("stake nullifier tree full")
	case err != nil:
		return err
	}
	if err := k.StakeNfSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeStakeNullifier,
		sdk.NewAttribute(types.AttributeKeyNullifier, hex.EncodeToString(nf)),
		sdk.NewAttribute(types.AttributeKeyIndex, strconv.FormatUint(idx, 10)),
	))
	return nil
}

// recordNfRoot records the nullifier tree's root and size at the end of a
// block that changed it (with recordStakeRoot: a snapshot then takes both
// trees as of the end of the same block).
func (k Keeper) recordNfRoot(ctx context.Context) error {
	t, err := k.nfTree(ctx)
	if err != nil {
		return err
	}
	_, size, err := k.latestNfRoot(ctx)
	if err != nil {
		return err
	}
	if t.Size() == size {
		return nil
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	if err := k.StakeNfLatestRoot.Set(ctx, privacy.FieldBytes(root)); err != nil {
		return err
	}
	return k.StakeNfLatestSize.Set(ctx, t.Size())
}

// latestNfRoot is the recorded nullifier root and size (indexed.EmptyRoot,
// 0 before the first insert was recorded).
func (k Keeper) latestNfRoot(ctx context.Context) ([]byte, uint64, error) {
	root, err := k.StakeNfLatestRoot.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return privacy.FieldBytes(indexed.EmptyRoot), 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	size, err := k.StakeNfLatestSize.Get(ctx)
	if err != nil {
		return nil, 0, err
	}
	return root, size, nil
}

// StakeNullifierTree is the nullifier tree's size, current root and latest
// recorded root.
func (k Keeper) StakeNullifierTree(ctx context.Context) (uint64, []byte, []byte, error) {
	t, err := k.nfTree(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	root, err := t.Root()
	if err != nil {
		return 0, nil, nil, err
	}
	latest, _, err := k.latestNfRoot(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	return t.Size(), privacy.FieldBytes(root), latest, nil
}
