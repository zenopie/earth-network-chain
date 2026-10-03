// Package indexed is the stake nullifier tree: an indexed (sorted, Aztec
// style) Merkle tree over zk/merkle's depth-32 Poseidon2 tree, so a circuit
// can prove a value is NOT in it (circuits/vote, privacy_core::
// assert_not_in_indexed).
//
// Leaf i holds one inserted value and its successor in numeric order:
//
//	leaf = H(TAG_SNFL, value, next_value, next_index)    (privacy.NFLeaf)
//
// next_value = 0 (next_index = 0) for the largest value. Leaf 0 is the
// sentinel, value 0, written by the first insert; the tree is a sorted linked
// list from it. Leaves are appended in insertion order. Inserting v appends
// (v, succ(v)) at the next index and repoints v's predecessor ("low leaf") to
// v: two O(depth) path updates. A value v is absent iff some leaf has
// value < v < next_value (or next_value = 0): its low leaf. Empty slots are 0,
// which no tagged hash equals, so an empty slot is never a leaf.
//
// Values are canonical field elements compared as integers; their 32-byte
// big-endian encodings sort the same way, so a byte-ordered KV index serves
// the predecessor and successor lookups (Index).
package indexed

import (
	"errors"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// Index maps every inserted value (never the sentinel 0) to its leaf index,
// ordered by value.
type Index interface {
	// Get is v's leaf index, ok=false if v was never inserted.
	Get(v fr.Element) (uint64, bool, error)
	// Below is the largest inserted value < v, ok=false if none.
	Below(v fr.Element) (fr.Element, uint64, bool, error)
	// Above is the smallest inserted value > v, ok=false if none.
	Above(v fr.Element) (fr.Element, uint64, bool, error)
	// Put records v at index.
	Put(v fr.Element, index uint64) error
}

var (
	ErrExists = errors.New("indexed: value already in the tree")
	ErrZero   = errors.New("indexed: 0 is the sentinel, never a value")
)

// Leaf is one leaf's preimage.
type Leaf struct {
	Value, NextValue fr.Element
	NextIndex        uint64
}

// Hash is the leaf as the tree stores it.
func (l Leaf) Hash() fr.Element { return privacy.NFLeaf(l.Value, l.NextValue, l.NextIndex) }

// Sentinel is leaf 0 of a tree holding nothing else.
var Sentinel = Leaf{}

// EmptyRoot is the root of a tree holding only the sentinel: the root of a
// tree nothing was inserted in (Size 0 or 1).
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

// Tree is an indexed tree over a node store and a value index. size is the
// leaf count, sentinel included (0 before the first insert); the caller
// persists it.
type Tree struct {
	nodes *merkle.Tree
	index Index
}

// New returns the tree with size leaves over nodes and index.
func New(nodes merkle.NodeStore, index Index, size uint64) *Tree {
	return &Tree{nodes: merkle.New(nodes, size), index: index}
}

// NewMem is an empty in-memory tree (tests, wallets' reference).
func NewMem() *Tree { return New(merkle.MemStore{}, NewMemIndex(), 0) }

// Size is the leaf count, the sentinel included once anything was inserted.
func (t *Tree) Size() uint64 { return t.nodes.Size() }

// Root is the current root (EmptyRoot before the first insert).
func (t *Tree) Root() (fr.Element, error) {
	if t.Size() == 0 {
		return EmptyRoot, nil
	}
	return t.nodes.Root()
}

// Has reports whether v was inserted.
func (t *Tree) Has(v fr.Element) (bool, error) {
	_, ok, err := t.index.Get(v)
	return ok, err
}

// neighbours are v's low leaf (predecessor, or the sentinel) and successor.
func (t *Tree) neighbours(v fr.Element) (low Leaf, lowIndex uint64, err error) {
	lv, li, ok, err := t.index.Below(v)
	if err != nil {
		return low, 0, err
	}
	if ok {
		low.Value = lv
		lowIndex = li
	}
	nv, ni, ok, err := t.index.Above(v)
	if err != nil {
		return low, 0, err
	}
	if ok {
		low.NextValue, low.NextIndex = nv, ni
	}
	return low, lowIndex, nil
}

// Insert adds v (nonzero, not yet inserted) and returns its leaf index.
func (t *Tree) Insert(v fr.Element) (uint64, error) {
	if v.IsZero() {
		return 0, ErrZero
	}
	if has, err := t.Has(v); err != nil {
		return 0, err
	} else if has {
		return 0, ErrExists
	}
	if t.Size() == 0 {
		if _, err := t.nodes.Append(Sentinel.Hash()); err != nil {
			return 0, err
		}
	}
	if t.Size() >= merkle.Capacity {
		return 0, merkle.ErrFull
	}
	low, lowIndex, err := t.neighbours(v)
	if err != nil {
		return 0, err
	}
	idx := t.Size()
	leaf := Leaf{Value: v, NextValue: low.NextValue, NextIndex: low.NextIndex}
	low.NextValue, low.NextIndex = v, idx
	if err := t.nodes.Update(lowIndex, low.Hash()); err != nil {
		return 0, err
	}
	if _, err := t.nodes.Append(leaf.Hash()); err != nil {
		return 0, err
	}
	return idx, t.index.Put(v, idx)
}

// Witness is a proof that a value is not in the tree: its low leaf, the low
// leaf's index and its path (the vote circuit's low_* inputs).
type Witness struct {
	Low   Leaf
	Index uint64
	Path  [merkle.Depth]fr.Element
}

// NonMembership is v's non-membership witness against the current root.
func (t *Tree) NonMembership(v fr.Element) (Witness, error) {
	var w Witness
	if v.IsZero() {
		return w, ErrZero
	}
	if has, err := t.Has(v); err != nil {
		return w, err
	} else if has {
		return w, ErrExists
	}
	low, lowIndex, err := t.neighbours(v)
	if err != nil {
		return w, err
	}
	w.Low, w.Index = low, lowIndex
	if t.Size() == 0 {
		// Only the (implicit) sentinel: its siblings are the empty subtrees.
		for i := range w.Path {
			w.Path[i] = merkle.Zero[i]
		}
		return w, nil
	}
	w.Path, err = t.nodes.Path(lowIndex)
	return w, err
}

// Verify checks w proves v absent under root, as the circuit does.
func (w Witness) Verify(v, root fr.Element) bool {
	if merkle.RootFromPath(w.Low.Hash(), w.Index, w.Path) != root {
		return false
	}
	if w.Low.Value.Cmp(&v) >= 0 {
		return false
	}
	return w.Low.NextValue.IsZero() || v.Cmp(&w.Low.NextValue) < 0
}

// Rebuild is the tree of values inserted in order (a wallet's view of the
// chain's tree from its nullifier stream; Size = len(values)+1).
func Rebuild(values []fr.Element) (*Tree, error) {
	t := NewMem()
	for _, v := range values {
		if _, err := t.Insert(v); err != nil {
			return nil, err
		}
	}
	return t, nil
}
