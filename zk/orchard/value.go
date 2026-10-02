package orchard

import (
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"
)

// ValueCommit is an action's net value commitment, as the action circuit
// computes it:
//
//	cv = v_spend*G(asset_spend) - v_out*G(asset_out) + rcv*R
//
// v_spend, v_out are u64; rcv is a circuit Field (< r) lifted into the scalar
// field. The spend and the output may hold different assets: each term uses
// its own canonical base, so the two never mix.
func ValueCommit(assetSpend fr.Element, vSpend uint64, assetOut fr.Element, vOut uint64, rcv fr.Element) Point {
	cv := Mul(ValueBase(assetSpend), ScalarU64(vSpend))
	cv = Sub(cv, Mul(ValueBase(assetOut), ScalarU64(vOut)))
	return Add(cv, Mul(R, ScalarFromField(rcv)))
}

// BindingSigningKey is the wallet's bsk = sum of every action's rcv (mod n).
// Every rcv enters with a plus sign because the circuit already puts the
// output's value with a minus sign inside cv.
func BindingSigningKey(rcvs []fr.Element) gfr.Element {
	var bsk gfr.Element
	for _, r := range rcvs {
		s := ScalarFromField(r)
		bsk.Add(&bsk, &s)
	}
	return bsk
}
