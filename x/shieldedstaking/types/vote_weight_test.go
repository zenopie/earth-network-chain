package types

import (
	"testing"

	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"
)

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

// AUDIT3 F3: a vote's weight strings are bound only in canonical form
// (OptionsBytes re-renders LegacyDec.String()), so "0.5", "0.50" and
// "00.500000000000000000" would all bind the same sighash under different
// msg bytes. Only the canonical spelling is accepted.
func TestAudit3VoteWeightRespellingRefused(t *testing.T) {
	spell := func(a, b string) []*v1.WeightedVoteOption {
		return []*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: a}, {Option: v1.OptionNo, Weight: b}}
	}
	require.NoError(t, ValidateOptions(spell("0.500000000000000000", "0.500000000000000000")))
	require.NoError(t, ValidateOptions(v1.NewNonSplitVoteOption(v1.OptionYes)))
	for _, alt := range [][2]string{{"0.5", "0.5"}, {"0.50", "0.500000000000000000"},
		{"00.500000000000000000", "0.500000000000000000"}, {"0.500000000000000000", "000.5"}} {
		require.ErrorIs(t, ValidateOptions(spell(alt[0], alt[1])), ErrInvalidMsg, "%v", alt)
	}
	require.ErrorIs(t, ValidateOptions([]*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: "1"}}), ErrInvalidMsg)
}
