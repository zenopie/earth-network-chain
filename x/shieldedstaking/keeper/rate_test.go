package keeper

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestConversionsFavourThePool(t *testing.T) {
	i := math.NewInt
	// First delegation: 1:1.
	d, err := derthFor(i(1_000), i(0), i(0))
	require.NoError(t, err)
	require.Equal(t, i(1_000), d)
	// Backing 1,001 behind 1,000 derth: 500 ERTH buys floor(500*1000/1001).
	d, err = derthFor(i(500), i(1_001), i(1_000))
	require.NoError(t, err)
	require.Equal(t, i(499), d)
	// And 499 derth is worth floor(499*1001/1000) = 499 <= 500 paid.
	require.Equal(t, i(499), valueOf(i(499), i(1_001), i(1_000)))
	// Round trips never create value.
	for _, a := range []int64{1, 7, 999, 123_456_789} {
		d, err := derthFor(i(a), i(1_000_003), i(999_989))
		require.NoError(t, err)
		require.True(t, valueOf(d, i(1_000_003).AddRaw(a), i(999_989).Add(d)).LTE(i(a)))
	}
	// Slashed to nothing: delegation refused.
	_, err = derthFor(i(1), i(0), i(10))
	require.Error(t, err)
	require.True(t, rateOf(i(5), i(0)).Equal(math.LegacyOneDec()))
}

func TestShareSumsExactly(t *testing.T) {
	i := math.NewInt
	parts := share(i(100), []math.Int{i(1), i(1), i(1)})
	require.Equal(t, []math.Int{i(33), i(33), i(34)}, parts)
	parts = share(i(7), []math.Int{i(0), i(0)})
	require.Equal(t, i(7), parts[0].Add(parts[1]))
	parts = share(i(95), []math.Int{i(50), i(30), i(20)})
	require.Equal(t, i(95), parts[0].Add(parts[1]).Add(parts[2]))
}
