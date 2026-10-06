package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"github.com/earth-network/earth/x/pki/certs"
	"math/big"
	"sort"
)

// A register-circuit variant and the DSC key type it verifies.
type variant struct {
	name string
	// Exactly one of these is set.
	ec  *ecSpec
	rsa *rsaSpec
}

type ecSpec struct {
	curve    *weierstrass
	coordLen int
	// P-256 uses Noir's std secp256r1 verifier, which takes r‖s as one 64-byte
	// input; every other curve goes through noir-ecdsa, which takes r and s
	// separately.
	combinedSig bool
}

type rsaSpec struct {
	bits  int
	limbs int
}

func variants() map[string]variant {
	return map[string]variant{
		"lean_poa_p256_sha256":    {name: "lean_poa_p256_sha256", ec: &ecSpec{curve: p256(), coordLen: 32, combinedSig: true}},
		"lean_poa_rsa2048_sha256": {name: "lean_poa_rsa2048_sha256", rsa: &rsaSpec{bits: 2048, limbs: 18}},
	}
}

// weierstrass is a short-Weierstrass curve y² = x³ + ax + b over F_p.
//
// Go's elliptic.CurveParams hardcodes a = -3, which is true for the NIST curves
// but not for Brainpool, so this implementation carries `a` explicitly and a
// fixture on any curve signs the same way. Correctness is self-checking: the register
// circuit verifies the signature, so a bad scalar multiplication fails witness
// generation rather than producing a silently wrong fixture.
type weierstrass struct {
	name      string
	P, A, B   *big.Int
	Gx, Gy, N *big.Int
	byteLen   int
}

func p256() *weierstrass {
	c := elliptic.P256().Params()
	return &weierstrass{"P-256", c.P, new(big.Int).Sub(c.P, big.NewInt(3)), c.B, c.Gx, c.Gy, c.N, 32}
}

// --- generic short-Weierstrass arithmetic (affine, big.Int) ---

type point struct{ x, y *big.Int } // nil x,y = point at infinity

func (c *weierstrass) isInf(p point) bool { return p.x == nil }

func (c *weierstrass) add(p, q point) point {
	if c.isInf(p) {
		return q
	}
	if c.isInf(q) {
		return p
	}
	if p.x.Cmp(q.x) == 0 {
		if new(big.Int).Add(p.y, q.y).Mod(new(big.Int).Add(p.y, q.y), c.P).Sign() == 0 {
			return point{} // p == -q
		}
		return c.double(p)
	}
	// lambda = (qy - py) / (qx - px)
	num := new(big.Int).Sub(q.y, p.y)
	den := new(big.Int).Sub(q.x, p.x)
	lambda := new(big.Int).Mul(num, new(big.Int).ModInverse(den.Mod(den, c.P), c.P))
	lambda.Mod(lambda, c.P)
	return c.fromLambda(lambda, p, q.x)
}

func (c *weierstrass) double(p point) point {
	if c.isInf(p) || p.y.Sign() == 0 {
		return point{}
	}
	// lambda = (3x² + a) / 2y
	num := new(big.Int).Mul(big.NewInt(3), new(big.Int).Mul(p.x, p.x))
	num.Add(num, c.A)
	den := new(big.Int).Mul(big.NewInt(2), p.y)
	lambda := new(big.Int).Mul(num, new(big.Int).ModInverse(den.Mod(den, c.P), c.P))
	lambda.Mod(lambda, c.P)
	return c.fromLambda(lambda, p, p.x)
}

// fromLambda finishes a chord/tangent step: x3 = λ² - x1 - x2, y3 = λ(x1 - x3) - y1.
func (c *weierstrass) fromLambda(lambda big2, p point, x2 *big.Int) point {
	x3 := new(big.Int).Mul(lambda, lambda)
	x3.Sub(x3, p.x)
	x3.Sub(x3, x2)
	x3.Mod(x3, c.P)
	y3 := new(big.Int).Sub(p.x, x3)
	y3.Mul(y3, lambda)
	y3.Sub(y3, p.y)
	y3.Mod(y3, c.P)
	return point{x3, y3}
}

type big2 = *big.Int

func (c *weierstrass) scalarBaseMult(k *big.Int) point {
	res := point{}
	acc := point{new(big.Int).Set(c.Gx), new(big.Int).Set(c.Gy)}
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) == 1 {
			res = c.add(res, acc)
		}
		acc = c.double(acc)
	}
	return res
}

// generateKey picks a private scalar and returns it with its public point.
func (c *weierstrass) generateKey() (*big.Int, point, error) {
	for {
		d, err := rand.Int(rand.Reader, c.N)
		if err != nil {
			return nil, point{}, err
		}
		if d.Sign() == 0 {
			continue
		}
		return d, c.scalarBaseMult(d), nil
	}
}

// sign produces a low-s ECDSA signature over a pre-hashed message. Noir's
// verifiers reject high-s, so normalisation is mandatory, not cosmetic.
func (c *weierstrass) sign(d *big.Int, digest []byte) (r, s *big.Int, err error) {
	z := hashToInt(digest, c.N)
	for {
		k, err := rand.Int(rand.Reader, c.N)
		if err != nil {
			return nil, nil, err
		}
		if k.Sign() == 0 {
			continue
		}
		p := c.scalarBaseMult(k)
		r = new(big.Int).Mod(p.x, c.N)
		if r.Sign() == 0 {
			continue
		}
		kInv := new(big.Int).ModInverse(k, c.N)
		s = new(big.Int).Mul(r, d)
		s.Add(s, z)
		s.Mul(s, kInv)
		s.Mod(s, c.N)
		if s.Sign() == 0 {
			continue
		}
		if s.Cmp(new(big.Int).Rsh(c.N, 1)) > 0 {
			s = new(big.Int).Sub(c.N, s)
		}
		return r, s, nil
	}
}

// hashToInt takes the leftmost bits of the digest, per SEC1.
func hashToInt(hash []byte, n *big.Int) *big.Int {
	orderBits := n.BitLen()
	orderBytes := (orderBits + 7) / 8
	if len(hash) > orderBytes {
		hash = hash[:orderBytes]
	}
	ret := new(big.Int).SetBytes(hash)
	if excess := len(hash)*8 - orderBits; excess > 0 {
		ret.Rsh(ret, uint(excess))
	}
	return ret
}

// limbs splits a big integer into noir-bignum's 120-bit little-endian limbs.
func limbs(x *big.Int, count int) []string {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 120), big.NewInt(1))
	out := make([]string, count)
	for i := 0; i < count; i++ {
		limb := new(big.Int).And(new(big.Int).Rsh(x, uint(120*i)), mask)
		out[i] = fmt.Sprintf("\"0x%s\"", limb.Text(16))
	}
	return out
}

// barrettRedc is noir-bignum's reduction parameter: floor(2^(2*bits+6) / n).
func barrettRedc(n *big.Int, bits int) *big.Int {
	return new(big.Int).Div(new(big.Int).Lsh(big.NewInt(1), uint(2*bits+6)), n)
}

func padLeft(v *big.Int, n int) []byte {
	out := make([]byte, n)
	v.FillBytes(out)
	return out
}

// byteStrings renders bytes as the decimal strings noir's TOML reader expects.
func byteStrings(data []byte) []string {
	out := make([]string, len(data))
	for i, v := range data {
		out[i] = fmt.Sprintf("\"%d\"", v)
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ecdsaKeyFor presents a keypair to crypto/x509 for certificate issuance.
//
// Returns nil for Brainpool: crypto/x509 refuses to encode curves outside its
// named set, which is the same gap that made x/pki/certs necessary in the first
// place. Those variants get witness inputs and a proof but no certificate chain,
// so the chain-side DSC binding is exercised on the NIST and RSA variants.
func ecdsaKeyFor(c *weierstrass, d *big.Int, pub point) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	var std elliptic.Curve
	switch c.name {
	case "P-256":
		std = elliptic.P256()
	case "P-384":
		std = elliptic.P384()
	default:
		return nil, nil
	}
	pk := &ecdsa.PublicKey{Curve: std, X: pub.x, Y: pub.y}
	return &ecdsa.PrivateKey{PublicKey: *pk, D: d}, pk
}

// curveTag is the commitment domain tag for this variant's key algorithm.
//
// Read from x/pki/certs rather than restated here: the fixtures exist to prove
// the chain and the circuits agree, and a second copy of the table is a second
// thing that can drift from the circuits.
func (v variant) curveTag() (certs.CurveTag, error) {
	return certs.CurveTagByName(v.ec.curve.name)
}
