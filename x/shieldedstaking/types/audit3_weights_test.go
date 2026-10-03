package types

import (
	"testing"

	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"
)

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
