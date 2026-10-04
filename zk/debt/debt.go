// Package debt is the slash debt tree (ORCHARD_DESIGN.md 3.5): an
// indexed (sorted) Merkle tree over zk/merkle's depth-32 Poseidon2 tree with
// one row per SLASHED redelegation, so a circuit can read what a
// redelegation's exposure is still worth: the row's retained derth if the
// move was slashed, or all of it if the move is absent (circuits/stake and
// circuits/vote, privacy_core::debt_retained).
//
// Leaf i holds one row and its successor in numeric key order:
//
//	leaf = H(TAG_DEBTL, key, next_key, next_index, retained)    (privacy.DebtLeaf)
//
// key is the move key (the redelegation's credit nullifier, a nonzero field
// element), retained the derth its credited exposure is worth after every
// slash so far. next_key = 0 (next_index 0) for the largest key. Leaf 0 is
// the sentinel (0, smallest key, its index, 0), written by the first insert;
// leaves are appended in insertion order. A row's retained only ever falls
// (Set rewrites its leaf in place); rows are never removed, since a note may
// clear its exposure long after the move.
//
// A key k is absent iff some leaf has key < k < next_key (or next_key = 0):
// its low leaf. Empty slots are 0, which no tagged hash equals.
package debt

import (
	"errors"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// ErrZero: 0 is the sentinel's key, never a row's.
var ErrZero = errors.New("debt: 0 is the sentinel, never a key")

// Rows holds each row's retained derth by key.
type Rows interface {
	Retained(key fr.Element) (uint64, bool, error)
	SetRetained(key fr.Element, retained uint64) error
}

// Leaf is one leaf's preimage.
type Leaf struct {
	Key, NextKey fr.Element
	NextIndex    uint64
	Retained     uint64
}

// Hash is the leaf as the tree stores it.
func (l Leaf) Hash() fr.Element { return privacy.DebtLeaf(l.Key, l.NextKey, l.NextIndex, l.Retained) }

// Sentinel is leaf 0 of a tree holding nothing else.
var Sentinel = Leaf{}

// EmptyRoot is the root of a tree with no row (Size 0 or 1).
var EmptyRoot fr.Element

func init() {
	t := merkle.NewMem()
	if _, err := t.Append(Sentinel.Hash()); err != nil {
		panic(err)
	}
	r, err := t.Root()
	if err != nil {
		panic(err)
	}
	EmptyRoot = r
}

// Tree is the debt tree over a node store, a key index (zk/indexed's) and the
// rows' retained values. size is the leaf count, sentinel included (0 before
// the first row); the caller persists it.
type Tree struct {
	nodes *merkle.Tree
	index indexed.Index
	rows  Rows
}

// New returns the tree with size leaves.
func New(nodes merkle.NodeStore, index indexed.Index, rows Rows, size uint64) *Tree {
	return &Tree{nodes: merkle.New(nodes, size), index: index, rows: rows}
}

// NewMem is an empty in-memory tree (tests, wallets' reference).
func NewMem() *Tree { return New(merkle.MemStore{}, indexed.NewMemIndex(), MemRows{}, 0) }

// Size is the leaf count, the sentinel included once a row exists.
func (t *Tree) Size() uint64 { return t.nodes.Size() }

// Root is the current root (EmptyRoot before the first row).
func (t *Tree) Root() (fr.Element, error) {
	if t.Size() == 0 {
		return EmptyRoot, nil
	}
	return t.nodes.Root()
}

// leafOf is the full leaf of key (0: the sentinel) at index.
func (t *Tree) leafOf(key fr.Element) (Leaf, error) {
	l := Leaf{Key: key}
	if !key.IsZero() {
		r, ok, err := t.rows.Retained(key)
		if err != nil {
			return l, err
		}
		if !ok {
			return l, errors.New("debt: a row without its retained value")
		}
		l.Retained = r
	}
	nv, ni, ok, err := t.index.Above(key)
	if err != nil {
		return l, err
	}
	if ok {
		l.NextKey, l.NextIndex = nv, ni
	}
	return l, nil
}

// low is k's low leaf (the largest key below k, or the sentinel) and its
// index.
func (t *Tree) low(k fr.Element) (Leaf, uint64, error) {
	lv, li, ok, err := t.index.Below(k)
	if err != nil {
		return Leaf{}, 0, err
	}
	if !ok {
		lv, li = fr.Element{}, 0
	}
	l, err := t.leafOf(lv)
	return l, li, err
}

// Get is key's retained derth, ok=false if the move was never slashed.
func (t *Tree) Get(key fr.Element) (uint64, bool, error) { return t.rows.Retained(key) }

// Set records that key's exposure is worth retained: a new row, or the
// existing row's leaf rewritten. Returns the row's leaf index.
func (t *Tree) Set(key fr.Element, retained uint64) (uint64, error) {
	if key.IsZero() {
		return 0, ErrZero
	}
	if idx, ok, err := t.index.Get(key); err != nil {
		return 0, err
	} else if ok {
		if err := t.rows.SetRetained(key, retained); err != nil {
			return 0, err
		}
		l, err := t.leafOf(key)
		if err != nil {
			return 0, err
		}
		return idx, t.nodes.Update(idx, l.Hash())
	}
	if t.Size() == 0 {
		if _, err := t.nodes.Append(Sentinel.Hash()); err != nil {
			return 0, err
		}
	}
	if t.Size() >= merkle.Capacity {
		return 0, merkle.ErrFull
	}
	low, lowIndex, err := t.low(key)
	if err != nil {
		return 0, err
	}
	idx := t.Size()
	leaf := Leaf{Key: key, NextKey: low.NextKey, NextIndex: low.NextIndex, Retained: retained}
	low.NextKey, low.NextIndex = key, idx
	if err := t.nodes.Update(lowIndex, low.Hash()); err != nil {
		return 0, err
	}
	if _, err := t.nodes.Append(leaf.Hash()); err != nil {
		return 0, err
	}
	if err := t.rows.SetRetained(key, retained); err != nil {
		return 0, err
	}
	return idx, t.index.Put(key, idx)
}

// Witness reads a move key under a root: the key's own leaf if it has a row,
// else its low leaf (the circuits' debt_low_* inputs).
type Witness struct {
	Low   Leaf
	Index uint64
	Path  [merkle.Depth]fr.Element
}

// Lookup is key's witness against the current root.
func (t *Tree) Lookup(key fr.Element) (Witness, error) {
	var w Witness
	if key.IsZero() {
		return w, ErrZero
	}
	if idx, ok, err := t.index.Get(key); err != nil {
		return w, err
	} else if ok {
		if w.Low, err = t.leafOf(key); err != nil {
			return w, err
		}
		w.Index = idx
	} else if w.Low, w.Index, err = t.low(key); err != nil {
		return w, err
	}
	if t.Size() == 0 {
		// Only the (implicit) sentinel: its siblings are the empty subtrees.
		for i := range w.Path {
			w.Path[i] = merkle.Zero[i]
		}
		return w, nil
	}
	var err error
	w.Path, err = t.nodes.Path(w.Index)
	return w, err
}

// Retained is what exposed derth of the move key is worth by w under root, as
// the circuit computes it: ok=false if w proves nothing for key.
func (w Witness) Retained(key fr.Element, exposed uint64, root fr.Element) (uint64, bool) {
	if merkle.RootFromPath(w.Low.Hash(), w.Index, w.Path) != root {
		return 0, false
	}
	if w.Low.Key == key {
		if w.Low.Retained > exposed {
			return 0, false
		}
		return w.Low.Retained, true
	}
	if w.Low.Key.Cmp(&key) >= 0 {
		return 0, false
	}
	if !w.Low.NextKey.IsZero() && key.Cmp(&w.Low.NextKey) >= 0 {
		return 0, false
	}
	return exposed, true
}

// Row is a key and its retained derth.
type Row struct {
	Key      fr.Element
	Retained uint64
}

// Rebuild is the tree of rows inserted in order (genesis; a wallet's view
// from the chain's debt events), each with its latest retained.
func Rebuild(rows []Row) (*Tree, error) {
	t := NewMem()
	for _, r := range rows {
		if _, err := t.Set(r.Key, r.Retained); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// MemRows is an in-memory Rows.
type MemRows map[fr.Element]uint64

func (m MemRows) Retained(k fr.Element) (uint64, bool, error) {
	r, ok := m[k]
	return r, ok, nil
}

func (m MemRows) SetRetained(k fr.Element, r uint64) error {
	m[k] = r
	return nil
}
