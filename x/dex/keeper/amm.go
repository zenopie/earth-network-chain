package keeper

import (
	"fmt"
	"math/big"

	"cosmossdk.io/math"
)

// oneHundredPercent is the denominator used to turn a percentage into a fraction.
var oneHundredPercent = math.LegacyNewDec(100)

// feeOf returns the ERTH fee charged on amount for a swap fee expressed as a
// percentage (e.g. swapFee = 0.3 means 0.3%), rounded up: the protocol's
// favour, like every other rounding in the dex, so a small swap cannot pay
// nothing (audit 5 L-DX4). Never above amount (swap_fee <= 100%).
func feeOf(amount math.Int, swapFee math.LegacyDec) math.Int {
	return math.LegacyNewDecFromInt(amount).Mul(swapFee).Quo(oneHundredPercent).Ceil().TruncateInt()
}

// intSqrt returns the integer square root of a non-negative math.Int.
func intSqrt(i math.Int) math.Int {
	return math.NewIntFromBigInt(new(big.Int).Sqrt(i.BigInt()))
}

// initialShares returns the LP shares minted when a pool is first created.
// It follows the Uniswap-v2 convention of sqrt(erth * token).
//
// The product is taken in big.Int: math.Int's Mul panics past 256 bits, and
// the callers check the result against types.MaxPoolAmount rather than
// trusting the inputs to be in range.
func initialShares(reserveErth, reserveToken math.Int) math.Int {
	p := new(big.Int).Mul(reserveErth.BigInt(), reserveToken.BigInt())
	return math.NewIntFromBigInt(p.Sqrt(p))
}

// mulDiv returns floor(a*b/c), computed in big.Int so that the product never
// panics. It errors (never panics) on a non-positive c or a result past
// math.Int's range. a*b/c with b <= c is at most a, which is how every caller
// uses it, so the range error only fires on corrupt state.
func mulDiv(a, b, c math.Int) (math.Int, error) {
	return mulDivRound(a, b, c, false)
}

// mulDivUp is mulDiv rounded up: ceil(a*b/c).
func mulDivUp(a, b, c math.Int) (math.Int, error) {
	return mulDivRound(a, b, c, true)
}

func mulDivRound(a, b, c math.Int, up bool) (math.Int, error) {
	if a.IsNil() || b.IsNil() || c.IsNil() || !c.IsPositive() || a.IsNegative() || b.IsNegative() {
		return math.Int{}, fmt.Errorf("mulDiv(%s, %s, %s): invalid operands", a, b, c)
	}
	q, r := new(big.Int).QuoRem(new(big.Int).Mul(a.BigInt(), b.BigInt()), c.BigInt(), new(big.Int))
	if up && r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.BitLen() > math.MaxBitLen {
		return math.Int{}, fmt.Errorf("mulDiv(%s, %s, %s): result out of range", a, b, c)
	}
	return math.NewIntFromBigInt(q), nil
}

// hopResult is the outcome of a single ERTH<->token swap along the wheel.
//
// The swap fee is always denominated in ERTH (the hub). It is split in two: one
// half stays in the pool as LP earnings (poolFee), and the other half is burned
// (burnErth), permanently reducing ERTH supply.
type hopResult struct {
	amountOut       math.Int // net output to the trader (or to the next hop)
	burnErth        math.Int // ERTH to burn from the pool reserves
	feeErth         math.Int // the whole swap fee: burnErth plus the half left to LPs
	volumeErth      math.Int // ERTH throughput of the hop (for volume weighting)
	newReserveErth  math.Int
	newReserveToken math.Int
}

// splitFee splits an ERTH fee into the burned half and the pool (LP) half.
//
// The burn takes any odd unit, so burn + pool == fee exactly and a fee that
// cannot be split evenly resolves toward destroying supply rather than toward
// keeping it. The gas fee split in x/earth/keeper/fees.go rounds the same way,
// for the same reason: one rule for every fee on the chain, and the rounding
// never quietly favours a recipient over the burn.
func splitFee(feeErth math.Int) (burn, pool math.Int) {
	burn = feeErth.Add(math.OneInt()).Quo(math.NewInt(2))
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
		feeErth:    feeErth,
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
		feeErth:    feeErth,
		volumeErth: amountErthIn,
		// The full input enters the pool minus the burned half; the pool half of
		// the fee therefore stays behind as LP earnings.
		newReserveErth:  reserveErth.Add(amountErthIn).Sub(burn),
		newReserveToken: reserveToken.Sub(tokenOut),
	}
}
