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

	"github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/debt"
	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// The slash debt tree (zk/debt; ORCHARD_DESIGN.md 3.5, 8.7): one row per
// private redelegation a slash reached, keyed by its move key, holding what
// its credited exposure is still worth. A note labelled with the move clears
// its label at that value (circuits/stake) and votes it (circuits/vote);
// a move with no row is worth its whole exposure. Rows only change when a
// slash reaches a redelegation, at the start of a block (finishSlashWatch),
// so the current root is stable through a block's txs: stake proofs and votes
// prove against the current root, never an older one (a stale root could
// skip a slash). Rows are never removed. Stored:
//
//	DebtIndex     key -> leaf index (ordered by key: the low-leaf lookup)
//	DebtLeafKeys  leaf index -> key (insertion order: export, wallets)
//	DebtRetained  key -> retained
//	DebtNodes     the Merkle nodes; DebtSize the leaf count (sentinel included)

type debtNodeStore struct {
	ctx context.Context
	k   Keeper
}

var _ merkle.NodeStore = debtNodeStore{}

func (s debtNodeStore) Get(level int, index uint64) (fr.Element, bool, error) {
	bz, err := s.k.DebtNodes.Get(s.ctx, collections.Join(uint32(level), index))
	if errors.Is(err, collections.ErrNotFound) {
		return fr.Element{}, false, nil
	} else if err != nil {
		return fr.Element{}, false, err
	}
	e, err := privacy.FieldFromBytes(bz)
	return e, err == nil, err
}

func (s debtNodeStore) Set(level int, index uint64, v fr.Element) error {
	return s.k.DebtNodes.Set(s.ctx, collections.Join(uint32(level), index), privacy.FieldBytes(v))
}

// debtIndex is indexed.Index over DebtIndex / DebtLeafKeys.
type debtIndex struct {
	ctx context.Context
	k   Keeper
}

var _ indexed.Index = debtIndex{}

func (x debtIndex) Get(v fr.Element) (uint64, bool, error) {
	i, err := x.k.DebtIndex.Get(x.ctx, privacy.FieldBytes(v))
	if errors.Is(err, collections.ErrNotFound) {
		return 0, false, nil
	}
	return i, err == nil, err
}

func (x debtIndex) first(r collections.Ranger[[]byte]) (fr.Element, uint64, bool, error) {
	it, err := x.k.DebtIndex.Iterate(x.ctx, r)
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

func (x debtIndex) Below(v fr.Element) (fr.Element, uint64, bool, error) {
	return x.first(new(collections.Range[[]byte]).EndExclusive(privacy.FieldBytes(v)).Descending())
}

func (x debtIndex) Above(v fr.Element) (fr.Element, uint64, bool, error) {
	return x.first(new(collections.Range[[]byte]).StartExclusive(privacy.FieldBytes(v)))
}

func (x debtIndex) Put(v fr.Element, index uint64) error {
	b := privacy.FieldBytes(v)
	if err := x.k.DebtIndex.Set(x.ctx, b, index); err != nil {
		return err
	}
	return x.k.DebtLeafKeys.Set(x.ctx, index, b)
}

// debtRows is debt.Rows over DebtRetained.
type debtRows struct {
	ctx context.Context
	k   Keeper
}

func (r debtRows) Retained(key fr.Element) (uint64, bool, error) {
	v, err := r.k.DebtRetained.Get(r.ctx, privacy.FieldBytes(key))
	if errors.Is(err, collections.ErrNotFound) {
		return 0, false, nil
	}
	return v, err == nil, err
}

func (r debtRows) SetRetained(key fr.Element, retained uint64) error {
	return r.k.DebtRetained.Set(r.ctx, privacy.FieldBytes(key), retained)
}

func (k Keeper) debtTree(ctx context.Context) (*debt.Tree, error) {
	size, err := k.DebtSize.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		size = 0
	} else if err != nil {
		return nil, err
	}
	return debt.New(debtNodeStore{ctx: ctx, k: k}, debtIndex{ctx: ctx, k: k}, debtRows{ctx: ctx, k: k}, size), nil
}

// DebtRoot is the slash debt tree's current root and leaf count.
func (k Keeper) DebtRoot(ctx context.Context) ([]byte, uint64, error) {
	t, err := k.debtTree(ctx)
	if err != nil {
		return nil, 0, err
	}
	r, err := t.Root()
	if err != nil {
		return nil, 0, err
	}
	return privacy.FieldBytes(r), t.Size(), nil
}

// setDebtRow records that the exposure of the move key is worth retained,
// emitting the row with its leaf index and the new root.
func (k Keeper) setDebtRow(ctx context.Context, key []byte, retained uint64) error {
	v, err := privacy.FieldFromBytes(key)
	if err != nil {
		return types.ErrStakeTree.Wrapf("move key: %v", err)
	}
	t, err := k.debtTree(ctx)
	if err != nil {
		return err
	}
	idx, err := t.Set(v, retained)
	if err != nil {
		return err
	}
	if err := k.DebtSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeDebtRow,
		sdk.NewAttribute(types.AttributeKeyMoveKey, hex.EncodeToString(key)),
		sdk.NewAttribute(types.AttributeKeyRetained, strconv.FormatUint(retained, 10)),
		sdk.NewAttribute(types.AttributeKeyIndex, strconv.FormatUint(idx, 10)),
		sdk.NewAttribute(types.AttributeKeyRoot, hex.EncodeToString(privacy.FieldBytes(root))),
	))
	return nil
}

// checkDebtRoot refuses a debt root that is not the current one.
func (k Keeper) checkDebtRoot(ctx context.Context, root []byte) error {
	cur, _, err := k.DebtRoot(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, root) {
		return types.ErrStakeTree.Wrapf("debt root %X is not the current slash debt root %X (a slash reached a redelegation since: re-prove)", root, cur)
	}
	return nil
}

// DebtRows lists up to limit rows from leaf index start+1 on, in insertion
// order, each with its latest retained value.
func (k Keeper) DebtRows(ctx context.Context, start, limit uint64) ([]types.DebtRow, error) {
	var rows []types.DebtRow
	err := k.DebtLeafKeys.Walk(ctx, new(collections.Range[uint64]).StartInclusive(start+1),
		func(_ uint64, key []byte) (bool, error) {
			r, err := k.DebtRetained.Get(ctx, key)
			if err != nil {
				return true, err
			}
			rows = append(rows, types.DebtRow{Key: key, Retained: r})
			return uint64(len(rows)) >= limit, nil
		})
	return rows, err
}
