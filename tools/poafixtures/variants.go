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
		"lean_poa":              {name: "lean_poa", ec: &ecSpec{curve: p256(), coordLen: 32, combinedSig: true}},
		"lean_poa_p384":         {name: "lean_poa_p384", ec: &ecSpec{curve: p384(), coordLen: 48}},
		"lean_poa_brainpool256": {name: "lean_poa_brainpool256", ec: &ecSpec{curve: brainpoolP256r1(), coordLen: 32}},
		"lean_poa_brainpool384": {name: "lean_poa_brainpool384", ec: &ecSpec{curve: brainpoolP384r1(), coordLen: 48}},
		"lean_poa_brainpool512": {name: "lean_poa_brainpool512", ec: &ecSpec{curve: brainpoolP512r1(), coordLen: 64}},
		"lean_poa_rsa2048":      {name: "lean_poa_rsa2048", rsa: &rsaSpec{bits: 2048, limbs: 18}},
		"lean_poa_rsa4096":      {name: "lean_poa_rsa4096", rsa: &rsaSpec{bits: 4096, limbs: 35}},
	}
}

// weierstrass is a short-Weierstrass curve y² = x³ + ax + b over F_p.
//
// Go's elliptic.CurveParams hardcodes a = -3, which is true for the NIST curves
// but NOT for Brainpool — so signing on Brainpool needs an implementation that
// carries `a` explicitly. Correctness is self-checking here: the register
// circuit verifies the signature, so a bad scalar multiplication fails witness
// generation rather than producing a silently wrong fixture.
type weierstrass struct {
	name      string
	P, A, B   *big.Int
	Gx, Gy, N *big.Int
	byteLen   int
}

func hexInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("bad curve constant " + s)
	}
	return n
}

func p256() *weierstrass {
	c := elliptic.P256().Params()
	return &weierstrass{"P-256", c.P, new(big.Int).Sub(c.P, big.NewInt(3)), c.B, c.Gx, c.Gy, c.N, 32}
}

func p384() *weierstrass {
	c := elliptic.P384().Params()
	return &weierstrass{"P-384", c.P, new(big.Int).Sub(c.P, big.NewInt(3)), c.B, c.Gx, c.Gy, c.N, 48}
}

func brainpoolP256r1() *weierstrass {
	return &weierstrass{"brainpoolP256r1",
		hexInt("a9fb57dba1eea9bc3e660a909d838d726e3bf623d52620282013481d1f6e5377"),
		hexInt("7d5a0975fc2c3057eef67530417affe7fb8055c126dc5c6ce94a4b44f330b5d9"),
		hexInt("26dc5c6ce94a4b44f330b5d9bbd77cbf958416295cf7e1ce6bccdc18ff8c07b6"),
		hexInt("8bd2aeb9cb7e57cb2c4b482ffc81b7afb9de27e1e3bd23c23a4453bd9ace3262"),
		hexInt("547ef835c3dac4fd97f8461a14611dc9c27745132ded8e545c1d54c72f046997"),
		hexInt("a9fb57dba1eea9bc3e660a909d838d718c397aa3b561a6f7901e0e82974856a7"), 32}
}

func brainpoolP384r1() *weierstrass {
	return &weierstrass{"brainpoolP384r1",
		hexInt("8cb91e82a3386d280f5d6f7e50e641df152f7109ed5456b412b1da197fb71123acd3a729901d1a71874700133107ec53"),
		hexInt("7bc382c63d8c150c3c72080ace05afa0c2bea28e4fb22787139165efba91f90f8aa5814a503ad4eb04a8c7dd22ce2826"),
		hexInt("04a8c7dd22ce28268b39b55416f0447c2fb77de107dcd2a62e880ea53eeb62d57cb4390295dbc9943ab78696fa504c11"),
		hexInt("1d1c64f068cf45ffa2a63a81b7c13f6b8847a3e77ef14fe3db7fcafe0cbd10e8e826e03436d646aaef87b2e247d4af1e"),
		hexInt("8abe1d7520f9c2a45cb1eb8e95cfd55262b70b29feec5864e19c054ff99129280e4646217791811142820341263c5315"),
		hexInt("8cb91e82a3386d280f5d6f7e50e641df152f7109ed5456b31f166e6cac0425a7cf3ab6af6b7fc3103b883202e9046565"), 48}
}

func brainpoolP512r1() *weierstrass {
	return &weierstrass{"brainpoolP512r1",
		hexInt("aadd9db8dbe9c48b3fd4e6ae33c9fc07cb308db3b3c9d20ed6639cca703308717d4d9b009bc66842aecda12ae6a380e62881ff2f2d82c68528aa6056583a48f3"),
		hexInt("7830a3318b603b89e2327145ac234cc594cbdd8d3df91610a83441caea9863bc2ded5d5aa8253aa10a2ef1c98b9ac8b57f1117a72bf2c7b9e7c1ac4d77fc94ca"),
		hexInt("3df91610a83441caea9863bc2ded5d5aa8253aa10a2ef1c98b9ac8b57f1117a72bf2c7b9e7c1ac4d77fc94cadc083e67984050b75ebae5dd2809bd638016f723"),
		hexInt("81aee4bdd82ed9645a21322e9c4c6a9385ed9f70b5d916c1b43b62eef4d0098eff3b1f78e2d0d48d50d1687b93b97d5f7c6d5047406a5e688b352209bcb9f822"),
		hexInt("7dde385d566332ecc0eabfa9cf7822fdf209f70024a57b1aa000c55b881f8111b2dcde494a5f485e5bca4bd88a2763aed1ca2b2fa8f0540678cd1e0f3ad80892"),
		hexInt("aadd9db8dbe9c48b3fd4e6ae33c9fc07cb308db3b3c9d20ed6639cca70330870553e5c414ca92619418661197fac10471db1d381085ddaddb58796829ca90069"), 64}
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
	if v.rsa != nil {
		return certs.TagRSA, nil
	}
	return certs.CurveTagByName(v.ec.curve.name)
}
