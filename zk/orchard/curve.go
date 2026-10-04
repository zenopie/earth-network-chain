// Package orchard is the chain's half of the Orchard-style shielded bundle
// (ORCHARD_DESIGN.md): Grumpkin points as the action circuit computes them,
// the canonical value bases, value commitments, the binding signature and the
// bundle balance check.
//
// Grumpkin is Noir's embedded curve (std::embedded_curve_ops): y^2 = x^3 - 17
// over the BN254 scalar field, prime order n = the BN254 base-field modulus,
// so every point other than infinity generates the group (cofactor 1).
// Coordinates are therefore BN254 fr elements, exactly the circuits' Field.
// gnark-crypto's ecc/grumpkin implements it; its fp is the same field as
// bn254/fr (converted here through canonical bytes) and its fr is the scalar
// field of size n.
//
// Infinity is (0, 0), as in Noir and in gnark.
package orchard

import (
	"errors"
	"math/big"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/grumpkin"
	gfp "github.com/consensys/gnark-crypto/ecc/grumpkin/fp"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"

	"github.com/earth-network/earth/zk/privacy"
)

// Point is a Grumpkin point in affine coordinates.
type Point = grumpkin.G1Affine

var (
	// TagGen domain-separates the per-asset value bases (ValueBase).
	TagGen = tag("earth.gen")
	// TagCvR domain-separates the value-commitment randomness base R.
	TagCvR = tag("earth.cv.r")
	// TagBsig domain-separates the binding signature challenge.
	TagBsig = tag("earth.bsig")
	// TagBundle domain-separates the bundle digest the sighash binds.
	TagBundle = tag("earth.bundle")
)

func tag(s string) fr.Element {
	var e fr.Element
	e.SetBigInt(new(big.Int).SetBytes([]byte(s)))
	return e
}

// R is the value-commitment randomness base and the binding signature's
// base: HashToPoint(TagCvR, 0). Nothing-up-my-sleeve; no discrete log
// relative to any value base is known.
var R Point

// RCounter is the counter HashToPoint settled on for R.
var RCounter uint32

func init() {
	R, RCounter = HashToPoint(TagCvR, fr.Element{})
}

func toFp(e fr.Element) gfp.Element {
	b := e.Bytes()
	var out gfp.Element
	if err := out.SetBytesCanonical(b[:]); err != nil {
		panic(err) // same modulus: never
	}
	return out
}

func toFr(e gfp.Element) fr.Element {
	b := e.Bytes()
	var out fr.Element
	if err := out.SetBytesCanonical(b[:]); err != nil {
		panic(err)
	}
	return out
}

// HashToPoint is try-and-increment onto Grumpkin, the definition the action
// circuit verifies (privacy_core::value::hash_to_point):
//
//	for ctr = 0, 1, ...:
//	    x = Poseidon2(tag, input, ctr)
//	    if x^3 - 17 is a square: y = its root with y <= (p-1)/2; return (x, y)
//
// The chain always uses the least ctr. The circuit accepts any ctr (it cannot
// cheaply prove minimality), which is sound: each (tag, input, ctr) is an
// independent random point bound to one input, and only the sign of y, never
// the counter, yields a point with a known relation to another (-G).
func HashToPoint(t, input fr.Element) (Point, uint32) {
	for ctr := uint32(0); ; ctr++ {
		if p, ok := HashToPointAt(t, input, ctr); ok {
			return p, ctr
		}
	}
}

// HashToPointAt is HashToPoint's step for one counter; ok=false when x^3-17
// is not a square there.
func HashToPointAt(t, input fr.Element, ctr uint32) (Point, bool) {
	x := toFp(privacy.H(t, input, privacy.U64(uint64(ctr))))
	var rhs, b gfp.Element
	rhs.Square(&x).Mul(&rhs, &x)
	b.SetUint64(17)
	rhs.Sub(&rhs, &b)
	if rhs.Legendre() != 1 {
		return Point{}, false
	}
	var y gfp.Element
	y.Sqrt(&rhs)
	if y.LexicographicallyLargest() { // y > (p-1)/2
		y.Neg(&y)
	}
	return Point{X: x, Y: y}, true
}

// ValueBase is asset's value base G_a = HashToPoint(TagGen, asset_id), with
// the least counter. A pure function of the asset id, memoized: the chain
// derives the base of every balance it checks rather than storing it.
func ValueBase(asset fr.Element) Point {
	if p, ok := baseCache.Load(asset); ok {
		return p.(Point)
	}
	p, _ := HashToPoint(TagGen, asset)
	baseCache.Store(asset, p)
	return p
}

// baseCache maps an asset id to its value base. Unbounded, but keyed by
// asset ids the caller has admitted (x/shielded only asks for registered
// denoms) and 96 bytes an entry.
var baseCache sync.Map

// Canonical reports whether p's y is the canonical root (y <= (p-1)/2).
func Canonical(p Point) bool {
	return !p.Y.LexicographicallyLargest()
}

// XY is p's coordinates as circuit fields ((0,0) for infinity).
func XY(p Point) (fr.Element, fr.Element) { return toFr(p.X), toFr(p.Y) }

// ErrNotOnCurve is returned for coordinates that are not a Grumpkin point.
var ErrNotOnCurve = errors.New("orchard: not a Grumpkin point")

// PointFromXY parses circuit coordinates. (0,0) is infinity; anything else
// must satisfy y^2 = x^3 - 17.
func PointFromXY(x, y fr.Element) (Point, error) {
	p := Point{X: toFp(x), Y: toFp(y)}
	if !p.IsOnCurve() {
		return Point{}, ErrNotOnCurve
	}
	return p, nil
}

// PointFromBytes parses 64 bytes: x || y, each a canonical 32-byte big-endian
// field element.
func PointFromBytes(b []byte) (Point, error) {
	if len(b) != 64 {
		return Point{}, ErrNotOnCurve
	}
	x, err := privacy.FieldFromBytes(b[:32])
	if err != nil {
		return Point{}, err
	}
	y, err := privacy.FieldFromBytes(b[32:])
	if err != nil {
		return Point{}, err
	}
	return PointFromXY(x, y)
}

// PointBytes is x || y, 64 bytes.
func PointBytes(p Point) []byte {
	x, y := XY(p)
	return append(privacy.FieldBytes(x), privacy.FieldBytes(y)...)
}

// ScalarFromField lifts a circuit Field (< r) into the Grumpkin scalar field
// (size n > r), as EmbeddedCurveScalar::from_field does.
func ScalarFromField(e fr.Element) gfr.Element {
	var b big.Int
	e.BigInt(&b)
	var s gfr.Element
	s.SetBigInt(&b)
	return s
}

// ScalarU64 is v as a Grumpkin scalar.
func ScalarU64(v uint64) gfr.Element {
	var s gfr.Element
	s.SetUint64(v)
	return s
}

// Mul is s*P.
func Mul(p Point, s gfr.Element) Point {
	var b big.Int
	s.BigInt(&b)
	var out Point
	out.ScalarMultiplication(&p, &b)
	return out
}

// Add is P+Q.
func Add(p, q Point) Point {
	var out Point
	out.Add(&p, &q)
	return out
}

// Sub is P-Q.
func Sub(p, q Point) Point {
	var out Point
	out.Sub(&p, &q)
	return out
}

// Neg is -P.
func Neg(p Point) Point {
	var out Point
	out.Neg(&p)
	return out
}
