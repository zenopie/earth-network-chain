package merkle

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func el(v uint64) fr.Element {
	var e fr.Element
	e.SetUint64(v)
	return e
}

// root is tr's root, failing the test on error.
func root(t *testing.T, tr *Tree) fr.Element {
	t.Helper()
	r, err := tr.Root()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func leaf(t *testing.T, tr *Tree, i uint64) fr.Element {
	t.Helper()
	l, err := tr.Leaf(i)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// naiveRoot hashes a full level-by-level tree over leaves padded with zeros.
func naiveRoot(leaves []fr.Element) fr.Element {
	level := append([]fr.Element(nil), leaves...)
	for d := 0; d < Depth; d++ {
		if len(level)%2 == 1 {
			level = append(level, Zero[d])
		}
		next := make([]fr.Element, len(level)/2)
		for i := range next {
			next[i] = Node(level[2*i], level[2*i+1])
		}
		level = next
	}
	return level[0]
}

func TestZeroMatchesNoir(t *testing.T) {
	// privacy_core::test_empty_tree_root pins the same value.
	const want = "b59baa35b9dc267744f0ccb4e3b0255c1fc512460d91130c6bc19fb2668568d"
	if got := Zero[Depth].Text(16); got != want {
		t.Fatalf("Zero[32] = %s, want %s", got, want)
	}
	if tr := NewMem(); root(t, tr) != Zero[Depth] {
		t.Fatal("empty tree root != Zero[32]")
	}
}

func TestAppendUpdatePath(t *testing.T) {
	tr := NewMem()
	var leaves []fr.Element
	for i := uint64(0); i < 37; i++ {
		l := el(1000 + i)
		idx, err := tr.Append(l)
		if err != nil || idx != i {
			t.Fatalf("append %d: idx=%d err=%v", i, idx, err)
		}
		leaves = append(leaves, l)
		if root(t, tr) != naiveRoot(leaves) {
			t.Fatalf("root mismatch after %d appends", i+1)
		}
	}
	// zero two leaves, as the identity tree does on expiry
	for _, i := range []uint64{0, 17} {
		if err := tr.Update(i, fr.Element{}); err != nil {
			t.Fatal(err)
		}
		leaves[i] = fr.Element{}
	}
	if root(t, tr) != naiveRoot(leaves) {
		t.Fatal("root mismatch after zeroing")
	}
	for i := uint64(0); i < tr.Size(); i++ {
		sib, err := tr.Path(i)
		if err != nil {
			t.Fatal(err)
		}
		if RootFromPath(leaf(t, tr, i), i, sib) != root(t, tr) {
			t.Fatalf("path %d does not reach root", i)
		}
		if i%2 == 0 && RootFromPath(leaf(t, tr, i), i+1, sib) == root(t, tr) {
			t.Fatalf("path %d verifies at the wrong index", i)
		}
	}
	if _, err := tr.Path(tr.Size()); err != ErrOutOfRange {
		t.Fatal("path beyond size should fail")
	}
	if err := tr.Update(tr.Size(), el(1)); err != ErrOutOfRange {
		t.Fatal("update beyond size should fail")
	}
}

func TestHighIndex(t *testing.T) {
	// A tree resumed near capacity: the last slot's path and root.
	tr := New(MemStore{}, Capacity-1)
	if _, err := tr.Append(el(5)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Append(el(6)); err != ErrFull {
		t.Fatal("expected ErrFull")
	}
	sib, _ := tr.Path(Capacity - 1)
	if RootFromPath(el(5), Capacity-1, sib) != root(t, tr) {
		t.Fatal("last-slot path does not reach root")
	}
}
