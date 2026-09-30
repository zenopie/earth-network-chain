// Package merkle is the depth-32 binary Poseidon2 Merkle tree shared by the
// identity tree (x/personhood, updatable) and the note tree (x/shielded,
// append-only). It matches privacy_core::merkle_root in the Noir circuits:
//
//	node  = Poseidon2([left, right])      (no tag; arity 2)
//	empty = 0 leaf; zero[i+1] = H(zero[i], zero[i])
//	bit i of the leaf index = 1  <=>  the running node is the RIGHT child at level i
//
// A zeroed leaf is the empty value 0. No tagged leaf can hash to 0 without a
// Poseidon2 preimage, so a zeroed slot can never be proven a member.
//
// Nodes are held in a NodeStore so the keeper can back the tree with a KV
// store; MemStore is the in-memory implementation used by tests and tools.
package merkle

import (
	"errors"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/poseidon2"
)

// Depth is the tree height; capacity is 2^32 leaves.
const Depth = 32

// Capacity is the number of leaf slots.
const Capacity = uint64(1) << Depth

// Zero[i] is the root of an empty subtree of height i (Zero[0] = empty leaf = 0).
var Zero [Depth + 1]fr.Element

func init() {
	for i := 0; i < Depth; i++ {
		Zero[i+1] = Node(Zero[i], Zero[i])
	}
}

// Node hashes two children.
func Node(l, r fr.Element) fr.Element { return poseidon2.Hash([]fr.Element{l, r}) }

// NodeStore holds non-empty nodes. level 0 is leaves, level Depth is the root.
//
// Get reports ok=false for a node never written; the tree reads that as the
// empty subtree root Zero[level]. Errors are the backing store's (a KV store
// in the keeper) and abort the operation: a tree whose write half-landed is
// only safe because the keeper runs inside a cached, discard-on-error context.
type NodeStore interface {
	Get(level int, index uint64) (fr.Element, bool, error)
	Set(level int, index uint64, v fr.Element) error
}

// MemStore is a map-backed NodeStore. It never errors.
type MemStore map[[2]uint64]fr.Element

func (m MemStore) Get(level int, index uint64) (fr.Element, bool, error) {
	v, ok := m[[2]uint64{uint64(level), index}]
	return v, ok, nil
}

func (m MemStore) Set(level int, index uint64, v fr.Element) error {
	m[[2]uint64{uint64(level), index}] = v
	return nil
}

// Tree is an incremental sparse Merkle tree over a NodeStore.
type Tree struct {
	store NodeStore
	next  uint64 // next free leaf index (append cursor)
}

var (
	ErrFull       = errors.New("merkle: tree full")
	ErrOutOfRange = errors.New("merkle: leaf index not yet appended")
)

// New returns a tree over store whose append cursor is at next. The caller
// persists the cursor (Size) alongside the store.
func New(store NodeStore, next uint64) *Tree { return &Tree{store: store, next: next} }

// NewMem returns an empty in-memory tree.
func NewMem() *Tree { return New(MemStore{}, 0) }

// Size is the number of appended leaves (including zeroed ones).
func (t *Tree) Size() uint64 { return t.next }

func (t *Tree) node(level int, index uint64) (fr.Element, error) {
	v, ok, err := t.store.Get(level, index)
	if err != nil {
		return fr.Element{}, err
	}
	if !ok {
		return Zero[level], nil
	}
	return v, nil
}

// Root is the current root.
func (t *Tree) Root() (fr.Element, error) { return t.node(Depth, 0) }

// Append writes leaf at the next free index and returns that index.
func (t *Tree) Append(leaf fr.Element) (uint64, error) {
	if t.next >= Capacity {
		return 0, ErrFull
	}
	i := t.next
	if err := t.set(i, leaf); err != nil {
		return 0, err
	}
	t.next++
	return i, nil
}

// Update overwrites an appended leaf (the identity tree zeroes leaves with
// Update(i, fr.Element{})).
func (t *Tree) Update(index uint64, leaf fr.Element) error {
	if index >= t.next {
		return ErrOutOfRange
	}
	return t.set(index, leaf)
}

func (t *Tree) set(index uint64, leaf fr.Element) error {
	if err := t.store.Set(0, index, leaf); err != nil {
		return err
	}
	cur := leaf
	for lvl := 0; lvl < Depth; lvl++ {
		sib, err := t.node(lvl, index^1)
		if err != nil {
			return err
		}
		if index&1 == 0 {
			cur = Node(cur, sib)
		} else {
			cur = Node(sib, cur)
		}
		index >>= 1
		if err := t.store.Set(lvl+1, index, cur); err != nil {
			return err
		}
	}
	return nil
}

// Leaf returns the leaf at index (0 if empty).
func (t *Tree) Leaf(index uint64) (fr.Element, error) { return t.node(0, index) }

// Path returns the Depth siblings from leaf level upward.
func (t *Tree) Path(index uint64) ([Depth]fr.Element, error) {
	var sib [Depth]fr.Element
	if index >= t.next {
		return sib, ErrOutOfRange
	}
	for lvl := 0; lvl < Depth; lvl++ {
		v, err := t.node(lvl, index^1)
		if err != nil {
			return sib, err
		}
		sib[lvl] = v
		index >>= 1
	}
	return sib, nil
}

// RootFromPath recomputes a root the way the circuit does.
func RootFromPath(leaf fr.Element, index uint64, sib [Depth]fr.Element) fr.Element {
	cur := leaf
	for lvl := 0; lvl < Depth; lvl++ {
		if (index>>lvl)&1 == 0 {
			cur = Node(cur, sib[lvl])
		} else {
			cur = Node(sib[lvl], cur)
		}
	}
	return cur
}
