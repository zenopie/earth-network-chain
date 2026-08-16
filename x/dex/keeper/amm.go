package keeper

import (
	"math/big"

	"cosmossdk.io/math"
)

// oneHundredPercent is the denominator used to turn a percentage into a fraction.
var oneHundredPercent = math.LegacyNewDec(100)

// feeOf returns the ERTH fee charged on amount for a swap fee expressed as a
// percentage (e.g. swapFee = 0.3 means 0.3%). It truncates to an integer.
func feeOf(amount math.Int, swapFee math.LegacyDec) math.Int {
	return math.LegacyNewDecFromInt(amount).Mul(swapFee).Quo(oneHundredPercent).TruncateInt()
}

// intSqrt returns the integer square root of a non-negative math.Int.
func intSqrt(i math.Int) math.Int {
	return math.NewIntFromBigInt(new(big.Int).Sqrt(i.BigInt()))
}

// initialShares returns the LP shares minted when a pool is first created.
// It follows the Uniswap-v2 convention of sqrt(erth * token).
func initialShares(reserveErth, reserveToken math.Int) math.Int {
	return intSqrt(reserveErth.Mul(reserveToken))
}

// hopResult is the outcome of a single ERTH<->token swap along the wheel.
//
// The swap fee is always denominated in ERTH (the hub). It is split in two: one
// half stays in the pool as LP earnings (poolFee), and the other half is burned
// (burnErth), permanently reducing ERTH supply.
type hopResult struct {
	amountOut       math.Int // net output to the trader (or to the next hop)
	burnErth        math.Int // ERTH to burn from the pool reserves
	volumeErth      math.Int // ERTH throughput of the hop (for volume weighting)
	newReserveErth  math.Int
	newReserveToken math.Int
}

// splitFee splits an ERTH fee into the burned half and the pool (LP) half. The
// pool keeps any odd unit so that burn + pool == fee exactly.
func splitFee(feeErth math.Int) (burn, pool math.Int) {
	burn = feeErth.Quo(math.NewInt(2))
	pool = feeErth.Sub(burn)
	return burn, pool
}

// swapTokenForHub prices a spoke-token -> ERTH swap on a constant-product curve.
// The fee is taken from the ERTH output; half is burned, half stays in the pool.
func swapTokenForHub(reserveErth, reserveToken, amountTokenIn math.Int, swapFee math.LegacyDec) hopResult {
	grossErth := reserveErth.Mul(amountTokenIn).Quo(reserveToken.Add(amountTokenIn))
	feeErth := feeOf(grossErth, swapFee)
	burn, _ := splitFee(feeErth)
	userErth := grossErth.Sub(feeErth)

	return hopResult{
		amountOut:  userErth,
		burnErth:   burn,
		volumeErth: grossErth,
		// userErth leaves the pool (to trader/next hop) and burn is removed;
		// the pool half of the fee stays behind, boosting the ERTH reserve.
		newReserveErth:  reserveErth.Sub(userErth).Sub(burn),
		newReserveToken: reserveToken.Add(amountTokenIn),
	}
}

// swapHubForToken prices an ERTH -> spoke-token swap on a constant-product curve.
// The fee is taken from the ERTH input; half is burned, half stays in the pool.
func swapHubForToken(reserveErth, reserveToken, amountErthIn math.Int, swapFee math.LegacyDec) hopResult {
	feeErth := feeOf(amountErthIn, swapFee)
	burn, _ := splitFee(feeErth)
	effectiveIn := amountErthIn.Sub(feeErth)
	tokenOut := reserveToken.Mul(effectiveIn).Quo(reserveErth.Add(effectiveIn))

	return hopResult{
		amountOut:  tokenOut,
		burnErth:   burn,
		volumeErth: amountErthIn,
		// The full input enters the pool minus the burned half; the pool half of
		// the fee therefore stays behind as LP earnings.
		newReserveErth:  reserveErth.Add(amountErthIn).Sub(burn),
		newReserveToken: reserveToken.Sub(tokenOut),
	}
}
