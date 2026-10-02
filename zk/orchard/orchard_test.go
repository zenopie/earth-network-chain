package orchard

import (
	"crypto/rand"
	"errors"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/grumpkin"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"

	"github.com/earth-network/earth/zk/privacy"
)

func el(s string) fr.Element {
	var e fr.Element
	if _, err := e.SetString(s); err != nil {
		panic(err)
	}
	return e
}

func wantXY(t *testing.T, name string, p Point, x, y string) {
	t.Helper()
	gx, gy := XY(p)
	if gx != el(x) || gy != el(y) {
		t.Fatalf("%s = (%s, %s), want (%s, %s)", name, gx.String(), gy.String(), x, y)
	}
}

// Noir's EmbeddedCurvePoint::generator() is gnark's Grumpkin generator.
func TestNoirGeneratorIsGnarks(t *testing.T) {
	_, g := grumpkin.Generators()
	wantXY(t, "G", g, "1", "17631683881184975370165255887551781615748388533673675138860")
}

// The constants privacy_core::value pins (value.nr test_r_derivation,
// test_value_bases_match_go, test_point_arithmetic_matches_go).
func TestNoirVectors(t *testing.T) {
	erth, anml := privacy.AssetID("uerth"), privacy.AssetID("uanml")
	if RCounter != 0 {
		t.Fatalf("R counter %d", RCounter)
	}
	wantXY(t, "R", R,
		"0x07fc551d28471de4eb62cf956996e963f8a0877bb97afd963ad5f296973401ce",
		"0x17bbea25d2f097a980174168aaffe0d61e97da2edeb3eec90c3a5e8a3b9328a4")
	wantXY(t, "G_uerth", ValueBase(erth),
		"0x16cbb75ad1d0a4a6d9237c82bdc3c63ec0fc5d5f3c847c8c4832836c091eae68",
		"0x0cc11824c96520b6268b50038a557c8d213b15ddd3bb586f61ea0ef2747a0c5c")
	if _, c := HashToPoint(TagGen, anml); c != 3 {
		t.Fatalf("uanml counter %d, want 3", c)
	}
	wantXY(t, "G_uanml", ValueBase(anml),
		"0x18a0d714f8b0fc0cac3f1e90dda89ab60f20b1867a1e327e5b76fb704cf587a5",
		"0x1356a023e45ee34fc5d1cb2a02eb12dd38f7ed5afe0b25b30adbce930e443bd1")
	wantXY(t, "V1", ValueCommit(erth, 1_000_000, anml, 500, privacy.U64(7)),
		"0x0eeeab2eff032b7092fcb4671d7118f79ac8d4bbf024c02e66350bdaf5a8fc9d",
		"0x2a03b86e5fb0e98a9227c939b215da80bea36ef51204d60b95b627932563db60")
	wantXY(t, "V2", Add(R, ValueBase(erth)),
		"0x044778190901765d4f027e2382ba88063fdfdb74e273c11f439440f8dfa4ee16",
		"0x206474e0f7353228d22ad7c896c38e67dac395992d9c9b0372f2e7601e563dc1")
	var m1 fr.Element
	m1.SetInt64(-1)
	wantXY(t, "V3", ValueCommit(anml, ^uint64(0), anml, 1, m1),
		"0x0cc7252fc1726a374708a6a298521ad29730dfd14ca5c39fa0ce56b84c0efb41",
		"0x269a187559ea4f5c2082d61e3cef850b84f37f100cae309a0291000ff80f0607")
}

func TestHashToPointCanonical(t *testing.T) {
	for i := uint64(0); i < 200; i++ {
		in := privacy.U64(i)
		p, ctr := HashToPoint(TagGen, in)
		if !p.IsOnCurve() || p.IsInfinity() || !Canonical(p) {
			t.Fatalf("input %d: bad point", i)
		}
		for c := uint32(0); c < ctr; c++ {
			if _, ok := HashToPointAt(TagGen, in, c); ok {
				t.Fatalf("input %d: counter %d not least", i, c)
			}
		}
		if neg := Neg(p); Canonical(neg) {
			t.Fatalf("input %d: both roots canonical", i)
		}
	}
	if ValueBase(privacy.U64(1)) == R {
		t.Fatal("value base equals R")
	}
}

func TestPointBytesRoundTrip(t *testing.T) {
	p := ValueBase(privacy.AssetID("uerth"))
	q, err := PointFromBytes(PointBytes(p))
	if err != nil || !q.Equal(&p) {
		t.Fatalf("round trip: %v", err)
	}
	var inf Point
	if q, err := PointFromBytes(PointBytes(inf)); err != nil || !q.IsInfinity() {
		t.Fatalf("infinity: %v", err)
	}
	bad := PointBytes(p)
	bad[63] ^= 1
	if _, err := PointFromBytes(bad); !errors.Is(err, ErrNotOnCurve) {
		t.Fatalf("off-curve accepted: %v", err)
	}
}

func randField(t *testing.T) fr.Element {
	var e fr.Element
	if _, err := e.SetRandom(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBindingSignature(t *testing.T) {
	bsk := ScalarFromField(randField(t))
	bvk := Mul(R, bsk)
	msg := randField(t)
	sig, err := SignBinding(bsk, msg, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinding(bvk, msg, sig); err != nil {
		t.Fatalf("valid: %v", err)
	}
	one := privacy.U64(1)
	other := msg
	other.Add(&other, &one)
	if VerifyBinding(bvk, other, sig) == nil {
		t.Fatal("other message accepted")
	}
	if VerifyBinding(Add(bvk, R), msg, sig) == nil {
		t.Fatal("other key accepted")
	}
	// s + n: same residue, second encoding, refused.
	s := new(big.Int).SetBytes(sig[64:])
	s.Add(s, gfr.Modulus())
	if s.BitLen() <= 256 {
		mal := append([]byte(nil), sig[:64]...)
		mal = append(mal, s.FillBytes(make([]byte, 32))...)
		if VerifyBinding(bvk, msg, mal) == nil {
			t.Fatal("non-canonical s accepted")
		}
	}
	// Rn at infinity, and the identity key, are refused.
	inf := append(make([]byte, 64), sig[64:]...)
	if VerifyBinding(bvk, msg, inf) == nil {
		t.Fatal("infinite nonce point accepted")
	}
	var zero gfr.Element
	sig0, _ := SignBinding(zero, msg, rand.Reader)
	if err := VerifyBinding(Point{}, msg, sig0); !errors.Is(err, ErrIdentityBvk) {
		t.Fatalf("identity bvk: %v", err)
	}
}

// action is a plain action for the balance tests.
type action struct {
	sAsset fr.Element
	sValue uint64
	oAsset fr.Element
	oValue uint64
	rcv    fr.Element
}

func bundleOf(t *testing.T, as []action, bals []Balance) (*Bundle, []fr.Element) {
	b := &Bundle{Anchor: privacy.U64(1), Balances: bals}
	rcvs := make([]fr.Element, len(as))
	for i, a := range as {
		rcvs[i] = a.rcv
		b.Actions = append(b.Actions, Action{
			Nullifier:  privacy.U64(uint64(100 + i)),
			Commitment: privacy.U64(uint64(200 + i)),
			Cv:         ValueCommit(a.sAsset, a.sValue, a.oAsset, a.oValue, a.rcv),
			Ciphertext: []byte{byte(i)},
		})
	}
	return b, rcvs
}

func sign(t *testing.T, b *Bundle, rcvs []fr.Element) fr.Element {
	sighash := Sighash("/test", "earth-1", []*Bundle{b})
	sig, err := SignBinding(BindingSigningKey(rcvs), sighash, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b.BindingSig = sig
	return sighash
}

func TestBundleBalance(t *testing.T) {
	erth, anml := privacy.AssetID("uerth"), privacy.AssetID("uanml")
	as := []action{
		{erth, 1_000_000, anml, 500, randField(t)},
		{anml, 700, erth, 990_000, randField(t)},
		{privacy.U64(9), 0, anml, 200, randField(t)}, // dummy spend, any asset
	}
	fee := []Balance{{Asset: erth, Value: 10_000}}
	b, rcvs := bundleOf(t, as, fee)
	sighash := sign(t, b, rcvs)
	if err := b.CheckBalance(sighash, CanonicalBase); err != nil {
		t.Fatalf("balanced bundle: %v", err)
	}
	if err := b.ValidateBasic(); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]Balance{
		"fee overstated":       {{Asset: erth, Value: 10_001}},
		"fee understated":      {{Asset: erth, Value: 9_999}},
		"fee in another asset": {{Asset: anml, Value: 10_000}},
		"extra balance":        {{Asset: erth, Value: 10_000}, {Asset: anml, Value: 1}},
		"no balance":           nil,
	}
	for name, bals := range cases {
		t.Run(name, func(t *testing.T) {
			c := *b
			c.Balances = bals
			// even re-signed with the real bsk, a wrong balance leaves a value
			// term in bvk the signer has no discrete log for.
			sh := sign(t, &c, rcvs)
			if err := c.CheckBalance(sh, CanonicalBase); !errors.Is(err, ErrBadBindingSig) {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("dropped action", func(t *testing.T) {
		c := *b
		c.Actions = c.Actions[:2]
		sh := sign(t, &c, rcvs[:2])
		if err := c.CheckBalance(sh, CanonicalBase); !errors.Is(err, ErrBadBindingSig) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("output inflated by one", func(t *testing.T) {
		bad := append([]action(nil), as...)
		bad[2].oValue++
		c, r := bundleOf(t, bad, fee)
		sh := sign(t, c, r)
		if err := c.CheckBalance(sh, CanonicalBase); !errors.Is(err, ErrBadBindingSig) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("sighash changed after signing", func(t *testing.T) {
		other := Sighash("/test", "earth-2", []*Bundle{b})
		if err := b.CheckBalance(other, CanonicalBase); !errors.Is(err, ErrBadBindingSig) {
			t.Fatalf("got %v", err)
		}
	})
}

// TestNegatedBaseInflation shows what the circuit's canonical-y check stops:
// two outputs of v, one under G_a and one under -G_a, commit to a net value
// of zero, and their binding signature VERIFIES. The chain cannot see this;
// only the action circuit's `non-canonical value base` assertion refuses it.
func TestNegatedBaseInflation(t *testing.T) {
	anml := privacy.AssetID("uanml")
	g := ValueBase(anml)
	r1, r2 := randField(t), randField(t)
	cv1 := Add(Neg(Mul(g, ScalarU64(1_000_000))), Mul(R, ScalarFromField(r1)))
	cv2 := Add(Neg(Mul(Neg(g), ScalarU64(1_000_000))), Mul(R, ScalarFromField(r2)))
	b := &Bundle{Anchor: privacy.U64(1), Actions: []Action{
		{Nullifier: privacy.U64(1), Cv: cv1}, {Nullifier: privacy.U64(2), Cv: cv2},
	}}
	sighash := sign(t, b, []fr.Element{r1, r2})
	if err := b.CheckBalance(sighash, CanonicalBase); err != nil {
		t.Fatalf("expected the forged balance to verify at the bvk level, got %v", err)
	}
}

func TestValidateBasic(t *testing.T) {
	erth := privacy.AssetID("uerth")
	b, rcvs := bundleOf(t, []action{{erth, 5, erth, 5, randField(t)}}, nil)
	sign(t, b, rcvs)
	if err := b.ValidateBasic(); err != nil {
		t.Fatal(err)
	}
	dup := *b
	dup.Actions = []Action{b.Actions[0], b.Actions[0]}
	if dup.ValidateBasic() == nil {
		t.Fatal("duplicate nullifier accepted")
	}
	zero := *b
	zero.Balances = []Balance{{Asset: erth, Value: 0}}
	if zero.ValidateBasic() == nil {
		t.Fatal("zero balance accepted")
	}
	empty := *b
	empty.Actions = nil
	if empty.ValidateBasic() == nil {
		t.Fatal("empty bundle accepted")
	}
	short := *b
	short.BindingSig = b.BindingSig[:95]
	if short.ValidateBasic() == nil {
		t.Fatal("short signature accepted")
	}
}

func TestDigestBindsEverything(t *testing.T) {
	erth := privacy.AssetID("uerth")
	b, _ := bundleOf(t, []action{{erth, 5, erth, 3, randField(t)}, {erth, 0, erth, 0, randField(t)}}, []Balance{{erth, 2}})
	d := b.Digest()
	mut := []func(c *Bundle){
		func(c *Bundle) { c.Anchor = privacy.U64(2) },
		func(c *Bundle) { c.Actions[0].Nullifier = privacy.U64(9) },
		func(c *Bundle) { c.Actions[0].Commitment = privacy.U64(9) },
		func(c *Bundle) { c.Actions[1].Cv = Add(c.Actions[1].Cv, R) },
		func(c *Bundle) { c.Actions[1].Ciphertext = []byte{7} },
		func(c *Bundle) { c.Actions[0], c.Actions[1] = c.Actions[1], c.Actions[0] },
		func(c *Bundle) { c.Balances[0].Value = 3 },
		func(c *Bundle) { c.Balances = nil },
	}
	for i, m := range mut {
		c := *b
		c.Actions = append([]Action(nil), b.Actions...)
		c.Balances = append([]Balance(nil), b.Balances...)
		m(&c)
		if c.Digest() == d {
			t.Fatalf("mutation %d not bound", i)
		}
	}
}
