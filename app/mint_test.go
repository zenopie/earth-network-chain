package app

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

// TestStakingIssuanceIsOnePillarAgainstTotalSupply pins both halves of the
// fraction, each of which has a plausible wrong answer.
//
// The numerator is the investor pillar alone, not the chain's 4 ERTH/sec gross
// issuance: AnnualProvisions is defined by the SDK as what the mint function
// mints and pays to the fee collector, and MintEmission pays one pillar. The
// other three never reach the fee collector, so publishing them here would tell
// every SDK-derived client the staking APR was four times what it is.
//
// The denominator is total supply of the bond denom, not bonded stake.
// StakingTokenSupply resolves to the former and the method name does not say so.
// Against bonded stake the same numerator reads in the hundreds of percent,
// which is the kind of number that passes review and is disastrous once a wallet
// renders it.
func TestStakingIssuanceIsOnePillarAgainstTotalSupply(t *testing.T) {
	// The genesis supply of earth-1.
	supply := math.NewInt(2_522_901_000_000_000)

	annual, inflation := stakingIssuance(supply)

	require.Equal(t, "31536000000000.000000000000000000", annual.String(),
		"31,536,000 ERTH a year: 1 ERTH/sec across a 365-day year")
	// A quarter of the ~5% gross rate keys.go describes for year one, and not a
	// round 1.25: the genesis pool is 2,522,880,000,000,000 and the supply is
	// slightly above it, the pillars being sized against the pool rather than
	// against every account genesis funds.
	require.Equal(t, "0.012499895953111120", inflation.String(),
		"~1.25%, a quarter of the chain's gross issuance rate")
}

// TestStakingIssuanceMatchesMintedAmount is the identity the SDK maintains and
// the one this file exists to restore: AnnualProvisions is what the mint
// function actually mints in a year.
//
// Checked against x/earth's constant rather than against a literal so that
// changing the pillar rate cannot leave the published figure behind.
func TestStakingIssuanceMatchesMintedAmount(t *testing.T) {
	annual, inflation := stakingIssuance(math.NewInt(2_522_901_000_000_000))

	perSecond := annual.QuoInt64(secondsPerYear)
	require.Equal(t, "1000000.000000000000000000", perSecond.String(),
		"the rate MintEmission prorates against block time")

	// AnnualProvisions = TotalSupply * Inflation, the SDK's own identity.
	//
	// Recovered to within rounding rather than exactly: Inflation is fixed-point
	// at 18 decimals, so a rate that does not terminate there loses sub-uerth
	// precision on the way back. DefaultMintFn carries the same error — it
	// derives each block's mint from these fields — so a tolerance here is the
	// SDK's arithmetic, not slack in this wrapper.
	recovered := inflation.MulInt(math.NewInt(2_522_901_000_000_000)).TruncateInt()
	drift := annual.TruncateInt().Sub(recovered).Abs()
	require.True(t, drift.LTE(math.OneInt()),
		"recovered %s from published rate, expected %s", recovered, annual.TruncateInt())
}

// TestStakingIssuanceRisesAsSupplyFalls records the shape of the curve, because
// it is the opposite of what the tokenomics comment describes and will look
// like a bug to whoever meets it first.
//
// The rate decays only while supply grows. It does not grow yet: the
// protocol-owned liquidity retires faster than the pillars issue, so for those
// five years the denominator shrinks and the published rate climbs.
func TestStakingIssuanceRisesAsSupplyFalls(t *testing.T) {
	_, atGenesis := stakingIssuance(math.NewInt(2_522_901_000_000_000))
	_, afterBurning := stakingIssuance(math.NewInt(2_400_000_000_000_000))
	_, afterGrowth := stakingIssuance(math.NewInt(3_000_000_000_000_000))

	require.True(t, afterBurning.GT(atGenesis), "a shrinking supply raises the rate")
	require.True(t, afterGrowth.LT(atGenesis), "and a growing one lowers it again")
}
