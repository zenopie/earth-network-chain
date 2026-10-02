package orchard

import (
	"crypto/sha512"
	"errors"
	"io"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"

	"github.com/earth-network/earth/zk/privacy"
)

// BindingSigSize is the binding signature's length: Rx || Ry || s, three
// 32-byte big-endian values.
const BindingSigSize = 96

// The binding signature is Schnorr over Grumpkin with base R:
//
//	bvk = bsk*R
//	sign:   k = nonce; Rn = k*R; e = Poseidon2(TAG_BSIG, Rn.x, Rn.y, bvk.x, bvk.y, sighash)
//	        s = k + e*bsk mod n
//	verify: s*R == Rn + e*bvk, Rn on the curve and not infinity, s < n
//
// e is a Poseidon2 output (< r < n), used directly as a scalar. Poseidon2 is
// the hash every client already carries for the circuits.

var (
	ErrBadBindingSig = errors.New("orchard: invalid binding signature")
	// ErrIdentityBvk refuses a bundle whose binding key is infinity: its
	// signature would be forgeable by anyone (bsk = 0), so its sighash would
	// bind nothing.
	ErrIdentityBvk = errors.New("orchard: binding verification key is the identity")
)

func challenge(rn, bvk Point, sighash fr.Element) gfr.Element {
	rx, ry := XY(rn)
	bx, by := XY(bvk)
	return ScalarFromField(privacy.H(TagBsig, rx, ry, bx, by, sighash))
}

// SignBinding signs sighash with bsk. The nonce is SHA-512(bsk || sighash ||
// 32 bytes of rnd) mod n (RedDSA's construction), so a broken rnd still never
// repeats a nonce across messages.
func SignBinding(bsk gfr.Element, sighash fr.Element, rnd io.Reader) ([]byte, error) {
	var t [32]byte
	if _, err := io.ReadFull(rnd, t[:]); err != nil {
		return nil, err
	}
	h := sha512.New()
	kb := bsk.Bytes()
	sb := sighash.Bytes()
	h.Write(kb[:])
	h.Write(sb[:])
	h.Write(t[:])
	var k gfr.Element
	k.SetBigInt(new(big.Int).SetBytes(h.Sum(nil)))
	if k.IsZero() {
		return nil, errors.New("orchard: zero nonce")
	}
	rn := Mul(R, k)
	bvk := Mul(R, bsk)
	e := challenge(rn, bvk, sighash)
	var s gfr.Element
	s.Mul(&e, &bsk).Add(&s, &k)
	out := PointBytes(rn)
	sbz := s.Bytes()
	return append(out, sbz[:]...), nil
}

// VerifyBinding checks sig on sighash under bvk.
func VerifyBinding(bvk Point, sighash fr.Element, sig []byte) error {
	if len(sig) != BindingSigSize {
		return ErrBadBindingSig
	}
	if bvk.IsInfinity() {
		return ErrIdentityBvk
	}
	rn, err := PointFromBytes(sig[:64])
	if err != nil || rn.IsInfinity() {
		return ErrBadBindingSig
	}
	var s gfr.Element
	if err := s.SetBytesCanonical(sig[64:]); err != nil {
		return ErrBadBindingSig // s >= n: one signature, one encoding
	}
	e := challenge(rn, bvk, sighash)
	lhs := Mul(R, s)
	rhs := Add(rn, Mul(bvk, e))
	if !lhs.Equal(&rhs) {
		return ErrBadBindingSig
	}
	return nil
}
