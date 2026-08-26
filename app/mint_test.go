package app

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

// TestGrossIssuanceIsAgainstTotalSupply pins the denominator.
//
// StakingTokenSupply resolves to the bank's total supply of the bond denom, not
// the bonded amount, and the method name does not say so. Against bonded stake
// the same numerator reads in the thousands of percent, which is the kind of
// number that passes review and is disastrous once a wallet renders it.
func TestGrossIssuanceIsAgainstTotalSupply(t *testing.T) {
	// The genesis supply of earth-1.
	supply := math.NewInt(2_522_901_000_000_000)

	annual, inflation := grossIssuance(supply)

	require.Equal(t, "126144000000000.000000000000000000", annual.String(),
		"126,144,000 ERTH a year: 4 ERTH/sec across a 365-day year")
	// 4.99996%, not a round 5: the genesis pool is 2,522,880,000,000,000 and the
	// supply is slightly above it, the pillars being sized against the pool
	// rather than against every account genesis funds.
	require.Equal(t, "0.049999583812444483", inflation.String(),
		"~5%, which is what keys.go says year one should be")
}

// TestGrossIssuanceRisesAsSupplyFalls records the shape of the curve, because
// it is the opposite of what the tokenomics comment describes and will look
// like a bug to whoever meets it first.
//
// The rate decays only while supply grows. It does not grow yet: the
// protocol-owned liquidity retires faster than the pillars issue, so for those
// five years the denominator shrinks and the published rate climbs.
func TestGrossIssuanceRisesAsSupplyFalls(t *testing.T) {
	_, atGenesis := grossIssuance(math.NewInt(2_522_901_000_000_000))
	_, afterBurning := grossIssuance(math.NewInt(2_400_000_000_000_000))
	_, afterGrowth := grossIssuance(math.NewInt(3_000_000_000_000_000))

	require.True(t, afterBurning.GT(atGenesis), "a shrinking supply raises the rate")
	require.True(t, afterGrowth.LT(atGenesis), "and a growing one lowers it again")
}
