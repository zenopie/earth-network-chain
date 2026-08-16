package poseidon2

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func elems(vs ...uint64) []fr.Element {
	out := make([]fr.Element, len(vs))
	for i, v := range vs {
		out[i].SetUint64(v)
	}
	return out
}

// TestHashMatchesNoir checks Hash against reference vectors produced by
// @zkpassport/poseidon2 (== noir-lang/poseidon v0.3.0, the circuit's hash).
func TestHashMatchesNoir(t *testing.T) {
	cases := []struct {
		name string
		in   []fr.Element
		want string
	}{
		{"[1,2]", elems(1, 2), "1594597865669602199208529098208508950092942746041644072252494753744672355203"},
		{"[0,0]", elems(0, 0), "5151499478991301833156025595048985053689893395646836724335623777508747990769"},
		{"[1,2,3]", elems(1, 2, 3), "16068223842875184682212183064520144190817798559788034419026031423767658184152"},
		{"[7]", elems(7), "18970562573323469175826317522388366048919495891674176618661784039580387947468"},
	}
	for _, c := range cases {
		got := Hash(c.in)
		if got.String() != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got.String(), c.want)
		}
	}

	// leaf-style: 64 bytes all = 0x01 (matches the circuit's DSC-key leaf shape).
	sixtyFour := make([]fr.Element, 64)
	for i := range sixtyFour {
		sixtyFour[i].SetUint64(1)
	}
	const want64 = "14295874367757759963211553815049736916613748207586589166074199566751511576828"
	if got := Hash(sixtyFour); got.String() != want64 {
		t.Errorf("64x0x01: got %s, want %s", got.String(), want64)
	}
}
