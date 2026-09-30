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
type NodeStore interface {
	Get(level int, index uint64) (fr.Element, bool)
	Set(level int, index uint64, v fr.Element)
}

// MemStore is a map-backed NodeStore.
type MemStore map[[2]uint64]fr.Element

func (m MemStore) Get(level int, index uint64) (fr.Element, bool) {
	v, ok := m[[2]uint64{uint64(level), index}]
	return v, ok
}

func (m MemStore) Set(level int, index uint64, v fr.Element) {
	m[[2]uint64{uint64(level), index}] = v
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

// New returns a tree over store whose append cursor is at next.
func New(store NodeStore, next uint64) *Tree { return &Tree{store: store, next: next} }

// NewMem returns an empty in-memory tree.
func NewMem() *Tree { return New(MemStore{}, 0) }

// Size is the number of appended leaves (including zeroed ones).
func (t *Tree) Size() uint64 { return t.next }

func (t *Tree) node(level int, index uint64) fr.Element {
	if v, ok := t.store.Get(level, index); ok {
		return v
	}
	return Zero[level]
}

// Root is the current root.
func (t *Tree) Root() fr.Element { return t.node(Depth, 0) }

// Append writes leaf at the next free index and returns that index.
func (t *Tree) Append(leaf fr.Element) (uint64, error) {
	if t.next >= Capacity {
		return 0, ErrFull
	}
	i := t.next
	t.next++
	t.set(i, leaf)
	return i, nil
}

// Update overwrites an appended leaf (the identity tree zeroes leaves with
// Update(i, fr.Element{})).
func (t *Tree) Update(index uint64, leaf fr.Element) error {
	if index >= t.next {
		return ErrOutOfRange
	}
	t.set(index, leaf)
	return nil
}

func (t *Tree) set(index uint64, leaf fr.Element) {
	t.store.Set(0, index, leaf)
	cur := leaf
	for lvl := 0; lvl < Depth; lvl++ {
		var l, r fr.Element
		if index&1 == 0 {
			l, r = cur, t.node(lvl, index^1)
		} else {
			l, r = t.node(lvl, index^1), cur
		}
		cur = Node(l, r)
		index >>= 1
		t.store.Set(lvl+1, index, cur)
	}
}

// Leaf returns the leaf at index (0 if empty).
func (t *Tree) Leaf(index uint64) fr.Element { return t.node(0, index) }

// Path returns the Depth siblings from leaf level upward.
func (t *Tree) Path(index uint64) ([Depth]fr.Element, error) {
	var sib [Depth]fr.Element
	if index >= t.next {
		return sib, ErrOutOfRange
	}
	for lvl := 0; lvl < Depth; lvl++ {
		sib[lvl] = t.node(lvl, index^1)
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
