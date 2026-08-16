package certs

import (
	"crypto/elliptic"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/asn1"
	"errors"
	"hash"
	"math/big"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// Curve is a short-Weierstrass curve y^2 = x^3 + a*x + b (mod p) with base point
// G and order n. Used for a generic (non-constant-time, verify-only) ECDSA that
// covers curves Go's stdlib rejects — Brainpool, and NIST curves encoded with
// explicit parameters — which together are ~31% of the real ICAO CSCA store.
type Curve struct {
	Name      string
	P, A, B   *big.Int
	Gx, Gy, N *big.Int
	byteLen   int
}

func mustHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("bad curve constant: " + s)
	}
	return n
}

// named curves keyed by their EC named-curve OID string.
var namedCurves = map[string]*Curve{
	"1.2.840.10045.3.1.7":   nistP256(), // prime256v1 / secp256r1
	"1.3.132.0.34":          nistP384(), // secp384r1
	"1.3.132.0.35":          nistP521(), // secp521r1
	"1.3.36.3.3.2.8.1.1.7":  brainpoolP256r1(),
	"1.3.36.3.3.2.8.1.1.11": brainpoolP384r1(),
	"1.3.36.3.3.2.8.1.1.13": brainpoolP512r1(),
}

// byPrime lets us recognise a NIST/Brainpool curve given only the field prime
// from explicit ECParameters.
var byPrime = func() map[string]*Curve {
	m := map[string]*Curve{}
	for _, c := range namedCurves {
		m[c.P.String()] = c
	}
	return m
}()

// NIST curves are derived from crypto/elliptic (Go supports them as named curves,
// so their parameters are guaranteed correct); a = p - 3 for all of them. Only
// the encoding (explicit params) is what stdlib x509 rejects — the math is fine.
func fromStd(name string, c elliptic.Curve) *Curve {
	p := c.Params()
	a := new(big.Int).Sub(p.P, big.NewInt(3))
	a.Mod(a, p.P)
	return &Curve{Name: name, P: p.P, A: a, B: p.B, Gx: p.Gx, Gy: p.Gy, N: p.N, byteLen: (p.BitSize + 7) / 8}
}
func nistP256() *Curve { return fromStd("P-256", elliptic.P256()) }
func nistP384() *Curve { return fromStd("P-384", elliptic.P384()) }
func nistP521() *Curve { return fromStd("P-521", elliptic.P521()) }
func brainpoolP256r1() *Curve {
	return &Curve{"brainpoolP256r1",
		mustHex("a9fb57dba1eea9bc3e660a909d838d726e3bf623d52620282013481d1f6e5377"),
		mustHex("7d5a0975fc2c3057eef67530417affe7fb8055c126dc5c6ce94a4b44f330b5d9"),
		mustHex("26dc5c6ce94a4b44f330b5d9bbd77cbf958416295cf7e1ce6bccdc18ff8c07b6"),
		mustHex("8bd2aeb9cb7e57cb2c4b482ffc81b7afb9de27e1e3bd23c23a4453bd9ace3262"),
		mustHex("547ef835c3dac4fd97f8461a14611dc9c27745132ded8e545c1d54c72f046997"),
		mustHex("a9fb57dba1eea9bc3e660a909d838d718c397aa3b561a6f7901e0e82974856a7"), 32}
}
func brainpoolP384r1() *Curve {
	return &Curve{"brainpoolP384r1",
		mustHex("8cb91e82a3386d280f5d6f7e50e641df152f7109ed5456b412b1da197fb71123acd3a729901d1a71874700133107ec53"),
		mustHex("7bc382c63d8c150c3c72080ace05afa0c2bea28e4fb22787139165efba91f90f8aa5814a503ad4eb04a8c7dd22ce2826"),
		mustHex("04a8c7dd22ce28268b39b55416f0447c2fb77de107dcd2a62e880ea53eeb62d57cb4390295dbc9943ab78696fa504c11"),
		mustHex("1d1c64f068cf45ffa2a63a81b7c13f6b8847a3e77ef14fe3db7fcafe0cbd10e8e826e03436d646aaef87b2e247d4af1e"),
		mustHex("8abe1d7520f9c2a45cb1eb8e95cfd55262b70b29feec5864e19c054ff99129280e4646217791811142820341263c5315"),
		mustHex("8cb91e82a3386d280f5d6f7e50e641df152f7109ed5456b31f166e6cac0425a7cf3ab6af6b7fc3103b883202e9046565"), 48}
}
func brainpoolP512r1() *Curve {
	return &Curve{"brainpoolP512r1",
		mustHex("aadd9db8dbe9c48b3fd4e6ae33c9fc07cb308db3b3c9d20ed6639cca703308717d4d9b009bc66842aecda12ae6a380e62881ff2f2d82c68528aa6056583a48f3"),
		mustHex("7830a3318b603b89e2327145ac234cc594cbdd8d3df91610a83441caea9863bc2ded5d5aa8253aa10a2ef1c98b9ac8b57f1117a72bf2c7b9e7c1ac4d77fc94ca"),
		mustHex("3df91610a83441caea9863bc2ded5d5aa8253aa10a2ef1c98b9ac8b57f1117a72bf2c7b9e7c1ac4d77fc94cadc083e67984050b75ebae5dd2809bd638016f723"),
		mustHex("81aee4bdd82ed9645a21322e9c4c6a9385ed9f70b5d916c1b43b62eef4d0098eff3b1f78e2d0d48d50d1687b93b97d5f7c6d5047406a5e688b352209bcb9f822"),
		mustHex("7dde385d566332ecc0eabfa9cf7822fdf209f70024a57b1aa000c55b881f8111b2dcde494a5f485e5bca4bd88a2763aed1ca2b2fa8f0540678cd1e0f3ad80892"),
		mustHex("aadd9db8dbe9c48b3fd4e6ae33c9fc07cb308db3b3c9d20ed6639cca70330870553e5c414ca92619418661197fac10471db1d381085ddaddb58796829ca90069"), 64}
}

// point arithmetic (affine, big.Int, mod P) --------------------------------

func (c *Curve) isOnCurve(x, y *big.Int) bool {
	if x.Sign() < 0 || x.Cmp(c.P) >= 0 || y.Sign() < 0 || y.Cmp(c.P) >= 0 {
		return false
	}
	// y^2 == x^3 + a*x + b
	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, c.P)
	rhs := new(big.Int).Mul(x, x)
	rhs.Mul(rhs, x)
	ax := new(big.Int).Mul(c.A, x)
	rhs.Add(rhs, ax)
	rhs.Add(rhs, c.B)
	rhs.Mod(rhs, c.P)
	return y2.Cmp(rhs) == 0
}

func (c *Curve) add(x1, y1, x2, y2 *big.Int) (*big.Int, *big.Int) {
	if x1 == nil {
		return x2, y2
	}
	if x2 == nil {
		return x1, y1
	}
	if x1.Cmp(x2) == 0 {
		if y1.Cmp(y2) != 0 || y1.Sign() == 0 {
			return nil, nil // P + (-P) = O
		}
		return c.double(x1, y1)
	}
	// lambda = (y2-y1)/(x2-x1)
	num := new(big.Int).Sub(y2, y1)
	den := new(big.Int).Sub(x2, x1)
	den.ModInverse(den.Mod(den, c.P), c.P)
	lam := num.Mul(num, den)
	lam.Mod(lam, c.P)
	x3 := new(big.Int).Mul(lam, lam)
	x3.Sub(x3, x1)
	x3.Sub(x3, x2)
	x3.Mod(x3, c.P)
	y3 := new(big.Int).Sub(x1, x3)
	y3.Mul(y3, lam)
	y3.Sub(y3, y1)
	y3.Mod(y3, c.P)
	return x3, y3
}

func (c *Curve) double(x1, y1 *big.Int) (*big.Int, *big.Int) {
	if y1.Sign() == 0 {
		return nil, nil
	}
	// lambda = (3x^2 + a) / (2y)
	num := new(big.Int).Mul(x1, x1)
	num.Mul(num, big.NewInt(3))
	num.Add(num, c.A)
	den := new(big.Int).Lsh(y1, 1)
	den.ModInverse(den.Mod(den, c.P), c.P)
	lam := num.Mul(num, den)
	lam.Mod(lam, c.P)
	x3 := new(big.Int).Mul(lam, lam)
	x3.Sub(x3, new(big.Int).Lsh(x1, 1))
	x3.Mod(x3, c.P)
	y3 := new(big.Int).Sub(x1, x3)
	y3.Mul(y3, lam)
	y3.Sub(y3, y1)
	y3.Mod(y3, c.P)
	return x3, y3
}

func (c *Curve) scalarMult(k, x, y *big.Int) (*big.Int, *big.Int) {
	var rx, ry *big.Int
	px, py := x, y
	// double-and-add, LSB first
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) == 1 {
			rx, ry = c.add(rx, ry, px, py)
		}
		px, py = c.double(px, py)
	}
	return rx, ry
}

// verifyECDSA verifies (r,s) over hash digest e under public key (x,y).
func (c *Curve) verifyECDSA(x, y, r, s *big.Int, digest []byte) bool {
	if !c.isOnCurve(x, y) {
		return false
	}
	if r.Sign() <= 0 || r.Cmp(c.N) >= 0 || s.Sign() <= 0 || s.Cmp(c.N) >= 0 {
		return false
	}
	e := hashToInt(digest, c.N)
	w := new(big.Int).ModInverse(s, c.N)
	if w == nil {
		return false
	}
	u1 := new(big.Int).Mul(e, w)
	u1.Mod(u1, c.N)
	u2 := new(big.Int).Mul(r, w)
	u2.Mod(u2, c.N)
	x1, y1 := c.scalarMult(u1, c.Gx, c.Gy)
	x2, y2 := c.scalarMult(u2, x, y)
	px, _ := c.add(x1, y1, x2, y2)
	if px == nil {
		return false
	}
	px.Mod(px, c.N)
	return px.Cmp(r) == 0
}

// hashToInt takes the leftmost bitlen(n) bits of the digest as an integer.
func hashToInt(digest []byte, n *big.Int) *big.Int {
	orderBits := n.BitLen()
	orderBytes := (orderBits + 7) / 8
	if len(digest) > orderBytes {
		digest = digest[:orderBytes]
	}
	ret := new(big.Int).SetBytes(digest)
	if excess := len(digest)*8 - orderBits; excess > 0 {
		ret.Rsh(ret, uint(excess))
	}
	return ret
}

// hashFor returns the hash implied by an ECDSA/RSA signature-algorithm OID.
func hashFor(oid asn1.ObjectIdentifier) (hash.Hash, bool) {
	switch oid.String() {
	case "1.2.840.10045.4.1", "1.2.840.113549.1.1.5": // ecdsa/rsa with SHA-1
		return sha1.New(), true
	case "1.2.840.10045.4.3.2", "1.2.840.113549.1.1.11": // SHA-256
		return sha256.New(), true
	case "1.2.840.10045.4.3.3", "1.2.840.113549.1.1.12": // SHA-384
		return sha512.New384(), true
	case "1.2.840.10045.4.3.4", "1.2.840.113549.1.1.13": // SHA-512
		return sha512.New(), true
	case "1.2.840.10045.4.3.1": // ecdsa-with-SHA224
		return sha256.New224(), true
	}
	return nil, false
}

// ecdsaSig is the DER SEQUENCE { r INTEGER, s INTEGER }.
type ecdsaSig struct{ R, S *big.Int }

func parseECDSASig(der []byte) (*big.Int, *big.Int, error) {
	var sig ecdsaSig
	input := cryptobyte.String(der)
	var seq cryptobyte.String
	if !input.ReadASN1(&seq, cbasn1.SEQUENCE) {
		return nil, nil, errors.New("bad ECDSA signature SEQUENCE")
	}
	sig.R, sig.S = new(big.Int), new(big.Int)
	if !seq.ReadASN1Integer(sig.R) || !seq.ReadASN1Integer(sig.S) {
		return nil, nil, errors.New("bad ECDSA signature integers")
	}
	return sig.R, sig.S, nil
}
