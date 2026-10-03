package indexed

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

func el(v uint64) fr.Element { return privacy.U64(v) }

// naiveRoot rebuilds the tree from the insertion order: each leaf points at
// its value's successor in the sorted set.
func naiveRoot(t *testing.T, order []fr.Element) fr.Element {
	idx := map[fr.Element]uint64{}
	for i, v := range order {
		idx[v] = uint64(i + 1)
	}
	sorted := append([]fr.Element(nil), order...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Cmp(&sorted[j]) < 0 })
	succ := map[fr.Element]Leaf{}
	all := append([]fr.Element{{}}, sorted...)
	for i, v := range all {
		l := Leaf{Value: v}
		if i+1 < len(all) {
			l.NextValue, l.NextIndex = all[i+1], idx[all[i+1]]
		}
		succ[v] = l
	}
	m := merkle.NewMem()
	_, err := m.Append(succ[fr.Element{}].Hash())
	require.NoError(t, err)
	for _, v := range order {
		_, err := m.Append(succ[v].Hash())
		require.NoError(t, err)
	}
	r, err := m.Root()
	require.NoError(t, err)
	return r
}

func TestEmpty(t *testing.T) {
	tr := NewMem()
	r, err := tr.Root()
	require.NoError(t, err)
	require.Equal(t, EmptyRoot, r)
	w, err := tr.NonMembership(el(5))
	require.NoError(t, err)
	require.True(t, w.Verify(el(5), r))
	require.Equal(t, uint64(0), w.Index)
	_, err = tr.Insert(fr.Element{})
	require.ErrorIs(t, err, ErrZero)
	_, err = tr.NonMembership(fr.Element{})
	require.ErrorIs(t, err, ErrZero)
}

func TestInsertMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tr := NewMem()
	var order []fr.Element
	seen := map[fr.Element]bool{}
	for i := 0; i < 60; i++ {
		var v fr.Element
		if i%3 == 0 {
			v = el(uint64(rng.Intn(1000) + 1)) // small, clustered
		} else {
			_, _ = v.SetRandom()
		}
		if seen[v] {
			_, err := tr.Insert(v)
			require.ErrorIs(t, err, ErrExists)
			continue
		}
		seen[v] = true
		idx, err := tr.Insert(v)
		require.NoError(t, err)
		order = append(order, v)
		require.Equal(t, uint64(len(order)), idx)
		require.Equal(t, uint64(len(order)+1), tr.Size())
		r, err := tr.Root()
		require.NoError(t, err)
		require.Equal(t, naiveRoot(t, order), r, "after %d inserts", len(order))
	}
	// Rebuild from the stream gives the same tree.
	rb, err := Rebuild(order)
	require.NoError(t, err)
	r1, _ := tr.Root()
	r2, _ := rb.Root()
	require.Equal(t, r1, r2)
}

func TestNonMembership(t *testing.T) {
	tr := NewMem()
	for _, v := range []uint64{10, 30, 20} {
		_, err := tr.Insert(el(v))
		require.NoError(t, err)
	}
	root, err := tr.Root()
	require.NoError(t, err)
	for _, v := range []uint64{1, 9, 11, 25, 29, 31, 1 << 40} {
		w, err := tr.NonMembership(el(v))
		require.NoError(t, err)
		require.True(t, w.Verify(el(v), root), "absent %d", v)
	}
	for _, v := range []uint64{10, 20, 30} {
		_, err := tr.NonMembership(el(v))
		require.ErrorIs(t, err, ErrExists)
	}
	// A witness taken before 25 was inserted no longer verifies after.
	w, err := tr.NonMembership(el(25))
	require.NoError(t, err)
	_, err = tr.Insert(el(25))
	require.NoError(t, err)
	root2, _ := tr.Root()
	require.False(t, w.Verify(el(25), root2))
	// ... but still does against the old root (the snapshot rule).
	require.True(t, w.Verify(el(25), root))
	// A present value's low leaf proves nothing: the value is its next.
	w2, err := tr.NonMembership(el(26))
	require.NoError(t, err)
	require.Equal(t, el(25), w2.Low.Value)
	require.False(t, w2.Verify(el(25), root2))
	// A forged leaf is not under the root.
	w2.Low.NextValue = el(1000)
	require.False(t, w2.Verify(el(27), root2))
}

// Vectors shared with privacy_core / circuits/vote (test_go_parity).
func TestNoirParity(t *testing.T) {
	require.Equal(t, "0x65617274682e736e666c", "0x"+privacy.TagSNFL.Text(16))
	require.Equal(t, "0x65617274682e766e66", "0x"+privacy.TagVNF.Text(16))
	leaf := privacy.NFLeaf(el(1), el(2), 3)
	vnf := privacy.VoteNF(el(0x5eed), el(0xa1), 1, 7)
	nf := privacy.StakeNF(el(0x5eed), el(0xa1), 1)
	var lo, hi fr.Element
	one := el(1)
	lo.Sub(&nf, &one)
	hi.Add(&nf, &one)
	tr := NewMem()
	for _, v := range []fr.Element{lo, hi} {
		_, err := tr.Insert(v)
		require.NoError(t, err)
	}
	snap, _ := tr.Root()
	w, err := tr.NonMembership(nf)
	require.NoError(t, err)
	require.Equal(t, uint64(1), w.Index)
	require.True(t, w.Verify(nf, snap))
	_, err = tr.Insert(nf)
	require.NoError(t, err)
	now, _ := tr.Root()
	got := map[string]string{
		"nf_leaf(1,2,3)":     "0x" + leaf.Text(16),
		"vote_nf(NK,a1,1,7)": "0x" + vnf.Text(16),
		"empty_root":         "0x" + EmptyRoot.Text(16),
		"nullifiers(false)":  "0x" + snap.Text(16),
		"nullifiers(true)":   "0x" + now.Text(16),
	}
	for k, v := range got {
		t.Logf("%s = %s", k, v)
	}
	want := map[string]string{
		"nf_leaf(1,2,3)":     goldenLeaf,
		"vote_nf(NK,a1,1,7)": goldenVNF,
		"empty_root":         goldenEmpty,
		"nullifiers(false)":  goldenSnap,
		"nullifiers(true)":   goldenNow,
	}
	require.Equal(t, want, got)
}

const (
	goldenLeaf  = "0xcdc3a81748c6389efaa3a6c29b7f4609a8e9f860230b70413e8bef512978276"
	goldenVNF   = "0x1ada84dad3e6afde3f370e97edf4df2ee4eeb6b1400d5c5f41882552f578ba2f"
	goldenEmpty = "0x18f5a2d2d3273f584793e90ac9bf77abf0ff2a05a101cd5943eaf7bbd0bd5b10"
	goldenSnap  = "0x154276da4092031ee0cc36041772a8ff6f563b14ae80df0d0f46ffe5b474cb6d"
	goldenNow   = "0x6a4a58053d288e995f3f876a1702085169ca86c6d739d072b94fc379a327048"
)
