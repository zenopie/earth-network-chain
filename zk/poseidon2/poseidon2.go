// Package poseidon2 implements the Barretenberg/Noir Poseidon2 hash over BN254
// (t=4, d=5, 8 full + 56 partial rounds) with the exact constants and sponge
// construction used by noir-lang/poseidon v0.3.0 (== @zkpassport/poseidon2, which
// the lean_poa circuit and registry-builder are validated against).
//
// This lets the chain build the DSC-registry Merkle tree with hashes identical to
// what the circuit verifies:
//
//	leaf = Hash(pubkey field-bytes)   node = Hash([left, right])
package poseidon2

import (
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

const (
	width         = 4                                         // t
	roundsFBegin  = 4                                         // rounds_f / 2
	roundsPartial = 56                                        // rounds_p
	roundsFEnd    = 4                                         // rounds_f / 2
	totalRounds   = roundsFBegin + roundsPartial + roundsFEnd // 64
)

var (
	matDiag4       [width]fr.Element
	roundConstants [totalRounds][width]fr.Element
)

func init() {
	for i := 0; i < width; i++ {
		if _, err := matDiag4[i].SetString(matDiag4Dec[i]); err != nil {
			panic("poseidon2: bad matDiag4 constant: " + err.Error())
		}
	}
	for r := 0; r < totalRounds; r++ {
		for i := 0; i < width; i++ {
			if _, err := roundConstants[r][i].SetString(roundConstantsDec[r][i]); err != nil {
				panic("poseidon2: bad round constant: " + err.Error())
			}
		}
	}
}

// sboxP applies x^5 in place.
func sboxP(x *fr.Element) {
	var x2, x4 fr.Element
	x2.Square(x)
	x4.Square(&x2)
	x.Mul(&x4, x)
}

// matmulExternal applies the Poseidon2 external (M4) matrix for t=4, in place.
func matmulExternal(s *[width]fr.Element) {
	var t0, t1, t2, t3, t4, t5, t6, t7 fr.Element
	t0.Add(&s[0], &s[1])
	t1.Add(&s[2], &s[3])
	t2.Add(&s[1], &s[1])
	t2.Add(&t2, &t1)
	t3.Add(&s[3], &s[3])
	t3.Add(&t3, &t0)
	t4.Add(&t1, &t1)
	t4.Add(&t4, &t4)
	t4.Add(&t4, &t3)
	t5.Add(&t0, &t0)
	t5.Add(&t5, &t5)
	t5.Add(&t5, &t2)
	t6.Add(&t3, &t5)
	t7.Add(&t2, &t4)
	s[0] = t6
	s[1] = t5
	s[2] = t7
	s[3] = t4
}

// matmulInternal applies the internal matrix (diagonal + all-ones) for t=4, in place.
func matmulInternal(s *[width]fr.Element) {
	var sum fr.Element
	sum.Add(&s[0], &s[1])
	sum.Add(&sum, &s[2])
	sum.Add(&sum, &s[3])
	for i := 0; i < width; i++ {
		s[i].Mul(&matDiag4[i], &s[i])
		s[i].Add(&s[i], &sum)
	}
}

// permute applies the Poseidon2 permutation to a t-element state, in place.
func permute(s *[width]fr.Element) {
	matmulExternal(s)
	// full rounds (beginning)
	for r := 0; r < roundsFBegin; r++ {
		for i := 0; i < width; i++ {
			s[i].Add(&s[i], &roundConstants[r][i])
			sboxP(&s[i])
		}
		matmulExternal(s)
	}
	// partial rounds
	pEnd := roundsFBegin + roundsPartial
	for r := roundsFBegin; r < pEnd; r++ {
		s[0].Add(&s[0], &roundConstants[r][0])
		sboxP(&s[0])
		matmulInternal(s)
	}
	// full rounds (end)
	for r := pEnd; r < totalRounds; r++ {
		for i := 0; i < width; i++ {
			s[i].Add(&s[i], &roundConstants[r][i])
			sboxP(&s[i])
		}
		matmulExternal(s)
	}
}

// rate is the sponge rate (t-1); capacity is 1.
const rate = width - 1

// Hash computes the Poseidon2 hash of the given field elements, matching
// Barretenberg/Noir's Poseidon2::hash (fixed-length, single field output).
func Hash(inputs []fr.Element) fr.Element {
	// domain IV = (len << 64) + (outLen - 1); outLen = 1 -> (len << 64).
	var iv fr.Element
	iv.SetUint64(uint64(len(inputs)))
	// shift left by 64 bits: multiply by 2^64.
	var twoTo64 fr.Element
	twoTo64.SetUint64(1)
	for i := 0; i < 64; i++ {
		twoTo64.Double(&twoTo64)
	}
	iv.Mul(&iv, &twoTo64)

	var state [width]fr.Element
	state[rate] = iv // capacity slot holds the IV

	var cache [rate]fr.Element
	cacheSize := 0

	duplex := func() {
		for i := cacheSize; i < rate; i++ {
			cache[i].SetZero()
		}
		for i := 0; i < rate; i++ {
			state[i].Add(&state[i], &cache[i])
		}
		permute(&state)
	}

	for _, in := range inputs {
		if cacheSize == rate {
			duplex()
			cache[0] = in
			cacheSize = 1
		} else {
			cache[cacheSize] = in
			cacheSize++
		}
	}
	// squeeze one element: perform a final duplex, output state[0].
	duplex()
	return state[0]
}
