package debt

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

// naiveRoot builds the tree from the insertion order and final values: each
// leaf points at its key's successor in the sorted set.
func naiveRoot(t *testing.T, order []fr.Element, retained map[fr.Element]uint64) fr.Element {
	idx := map[fr.Element]uint64{}
	for i, k := range order {
		idx[k] = uint64(i + 1)
	}
	sorted := append([]fr.Element(nil), order...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Cmp(&sorted[j]) < 0 })
	all := append([]fr.Element{{}}, sorted...)
	leaf := map[fr.Element]Leaf{}
	for i, k := range all {
		l := Leaf{Key: k, Retained: retained[k]}
		if i+1 < len(all) {
			l.NextKey, l.NextIndex = all[i+1], idx[all[i+1]]
		}
		leaf[k] = l
	}
	m := merkle.NewMem()
	_, err := m.Append(leaf[fr.Element{}].Hash())
	require.NoError(t, err)
	for _, k := range order {
		_, err := m.Append(leaf[k].Hash())
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
	w, err := tr.Lookup(el(5))
	require.NoError(t, err)
	got, ok := w.Retained(el(5), 9, r)
	require.True(t, ok)
	require.Equal(t, uint64(9), got, "an unslashed move keeps its exposure")
	_, err = tr.Set(fr.Element{}, 1)
	require.ErrorIs(t, err, ErrZero)
}

func TestSetMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	tr := NewMem()
	var order []fr.Element
	retained := map[fr.Element]uint64{}
	for i := 0; i < 60; i++ {
		var k fr.Element
		if len(order) > 0 && rng.Intn(3) == 0 {
			k = order[rng.Intn(len(order))] // a second slash of the same move
		} else {
			k = el(uint64(rng.Int63n(1<<40)) + 1)
			if _, seen := retained[k]; seen {
				continue
			}
			order = append(order, k)
		}
		r := uint64(rng.Int63n(1000))
		retained[k] = r
		_, err := tr.Set(k, r)
		require.NoError(t, err)
		root, err := tr.Root()
		require.NoError(t, err)
		require.Equal(t, naiveRoot(t, order, retained), root)
	}
	root, err := tr.Root()
	require.NoError(t, err)
	// Every row reads its retained; keys between rows read their exposure.
	for _, k := range order {
		w, err := tr.Lookup(k)
		require.NoError(t, err)
		got, ok := w.Retained(k, 1000, root)
		require.True(t, ok)
		require.Equal(t, retained[k], got)
		var gap fr.Element
		gap.Add(&k, &fr.Element{1})
		if _, slashed := retained[gap]; !slashed {
			w, err := tr.Lookup(gap)
			require.NoError(t, err)
			got, ok := w.Retained(gap, 1000, root)
			require.True(t, ok)
			require.Equal(t, uint64(1000), got)
		}
	}
	// Rebuild from the insertion order with the latest values: same tree.
	var rows []Row
	for _, k := range order {
		rows = append(rows, Row{Key: k, Retained: retained[k]})
	}
	re, err := Rebuild(rows)
	require.NoError(t, err)
	rr, err := re.Root()
	require.NoError(t, err)
	require.Equal(t, root, rr)
}

// The circuit's soundness conditions, mirrored: a slashed move cannot read
// itself as absent, nor read a stale tree, nor read another row.
func TestWitnessSoundness(t *testing.T) {
	tr := NewMem()
	_, err := tr.Set(el(10), 7)
	require.NoError(t, err)
	_, err = tr.Set(el(20), 3)
	require.NoError(t, err)
	root, err := tr.Root()
	require.NoError(t, err)

	own, err := tr.Lookup(el(10))
	require.NoError(t, err)
	got, ok := own.Retained(el(10), 9, root)
	require.True(t, ok)
	require.Equal(t, uint64(7), got)

	// The sentinel's leaf (low of 5) cannot prove 10 absent: 10 is its next.
	low5, err := tr.Lookup(el(5))
	require.NoError(t, err)
	_, ok = low5.Retained(el(10), 9, root)
	require.False(t, ok)
	// Row 20's leaf is above 10.
	row20, err := tr.Lookup(el(20))
	require.NoError(t, err)
	_, ok = row20.Retained(el(10), 9, root)
	require.False(t, ok)
	// A row's retained above the exposure is refused (never written by the
	// chain, which only cuts).
	_, ok = own.Retained(el(10), 6, root)
	require.False(t, ok)

	// The tree before 10's second slash: its witness fails the new root.
	_, err = tr.Set(el(10), 2)
	require.NoError(t, err)
	now, err := tr.Root()
	require.NoError(t, err)
	_, ok = own.Retained(el(10), 9, now)
	require.False(t, ok)
	fresh, err := tr.Lookup(el(10))
	require.NoError(t, err)
	got, ok = fresh.Retained(el(10), 9, now)
	require.True(t, ok)
	require.Equal(t, uint64(2), got)
}

// Pinned in circuits/privacy_core test_go_parity_debt.
func TestNoirParity(t *testing.T) {
	require.Equal(t, "0b28cc858d976ddad0ede75ca9538f9b5ab36538964f6e241b8e89be2711e82a", hexOf(privacy.DebtLeaf(el(1), el(2), 3, 4)))
	require.Equal(t, "2dfbc154973d1d3ec6e03137ba41b5c2cf5119f66cc69e3e58c033a77c80a881", hexOf(privacy.StakeLabel(el(0x4d4b), 1000, 200)))
	require.Equal(t, "0ffc538b4162732774bd5026e7a07bd00d2fe406af55231fa0c255c321ad4232", hexOf(privacy.StakeCM(el(1), 2, el(3), el(4))))
	require.Equal(t, "0cea3d3e26cd2710109d7cbff5bf48570ba54332f812d538893f0958007f6903", hexOf(EmptyRoot))
	tr := NewMem()
	_, err := tr.Set(el(10), 7)
	require.NoError(t, err)
	r, err := tr.Root()
	require.NoError(t, err)
	require.Equal(t, "2a7c0afbf11f7ecd5cf533fbb282bf8eb0bf5df39d697c0b3ac04699a80b15b7", hexOf(r))
}

func hexOf(e fr.Element) string {
	b := e.Bytes()
	const h = "0123456789abcdef"
	out := make([]byte, 64)
	for i, c := range b {
		out[2*i], out[2*i+1] = h[c>>4], h[c&15]
	}
	return string(out)
}
