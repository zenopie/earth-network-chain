package indexed_test

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
)

// Audit 4 (core PoC, ported): for random insert sequences: every inserted value has NO valid
// non-membership witness from any leaf (exhaustive over leaves), and every
// absent value has exactly one.
func TestIndexedSoundness(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for round := 0; round < 30; round++ {
		tr := indexed.NewMem()
		var vals []fr.Element
		n := 1 + r.Intn(12)
		for i := 0; i < n; i++ {
			var v fr.Element
			if r.Intn(3) == 0 {
				v.SetUint64(uint64(1 + r.Intn(40)))
			} else if r.Intn(2) == 0 {
				v.SetBigInt(new(big.Int).Rand(r, fr.Modulus()))
			} else { // near modulus
				v.SetUint64(uint64(1 + r.Intn(5)))
				v.Neg(&v)
			}
			if _, err := tr.Insert(v); err != nil {
				continue
			}
			vals = append(vals, v)
		}
		// rebuild leaf preimages by replaying in a shadow list
		type leaf = indexed.Leaf
		leaves := []leaf{{}}
		for _, v := range vals {
			// find low
			lo := 0
			for i, l := range leaves {
				if l.Value.Cmp(&v) < 0 && l.Value.Cmp(&leaves[lo].Value) >= 0 {
					lo = i
				}
			}
			nl := leaf{Value: v, NextValue: leaves[lo].NextValue, NextIndex: leaves[lo].NextIndex}
			leaves[lo].NextValue, leaves[lo].NextIndex = v, uint64(len(leaves))
			leaves = append(leaves, nl)
		}
		root, _ := tr.Root()
		// shadow tree root equals
		m := merkle.NewMem()
		for _, l := range leaves {
			m.Append(l.Hash())
		}
		mr, _ := m.Root()
		if mr != root {
			t.Fatalf("round %d: shadow root mismatch", round)
		}
		check := func(v fr.Element, member bool) {
			ok := 0
			for i, l := range leaves {
				p, _ := m.Path(uint64(i))
				w := indexed.Witness{Low: l, Index: uint64(i), Path: p}
				if w.Verify(v, root) {
					ok++
				}
			}
			if member && ok != 0 {
				t.Fatalf("member provable absent")
			}
			if !member && ok != 1 {
				t.Fatalf("absent value has %d witnesses", ok)
			}
		}
		for _, v := range vals {
			check(v, true)
		}
		for k := 0; k < 20; k++ {
			var v fr.Element
			v.SetBigInt(new(big.Int).Rand(r, fr.Modulus()))
			if has, _ := tr.Has(v); !has && !v.IsZero() {
				check(v, false)
				w, err := tr.NonMembership(v)
				if err != nil || !w.Verify(v, root) {
					t.Fatalf("honest witness fails")
				}
			}
		}
	}
}
