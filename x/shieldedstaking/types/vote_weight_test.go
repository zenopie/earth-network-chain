package types

import "testing"

// Audit 6 C-L3: weights carry at most three significant digits.
func TestVoteWeightSigFigs(t *testing.T) {
	for w, want := range map[uint64]uint64{
		1: 1, 999: 999, 1000: 1000, 1234: 1230, 399_999_999: 399_000_000, 120_000: 120_000,
		^uint64(0): 18_400_000_000_000_000_000,
	} {
		if got := RoundVoteWeight(w); got != want {
			t.Fatalf("RoundVoteWeight(%d) = %d, want %d", w, got, want)
		}
		if (CheckVoteWeight(w) == nil) != (w == want) {
			t.Fatalf("CheckVoteWeight(%d) disagrees with the rounding", w)
		}
	}
}
