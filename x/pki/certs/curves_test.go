package certs

import (
	"crypto/elliptic"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// TestCurveConstants sanity-checks every hardcoded curve: the generator must be on
// the curve and have the right order (n*G == O). Catches constant transcription
// errors (Brainpool constants are entered by hand).
func TestCurveConstants(t *testing.T) {
	for _, c := range []*Curve{nistP256(), nistP384(), nistP521(), brainpoolP256r1(), brainpoolP384r1(), brainpoolP512r1()} {
		if !c.isOnCurve(c.Gx, c.Gy) {
			t.Errorf("%s: generator not on curve", c.Name)
		}
		if x, _ := c.scalarMult(c.N, c.Gx, c.Gy); x != nil {
			t.Errorf("%s: n*G != O", c.Name)
		}
	}
	// The NIST curves must agree with crypto/elliptic.
	for _, p := range []struct {
		c   *Curve
		std elliptic.Curve
	}{{nistP256(), elliptic.P256()}, {nistP384(), elliptic.P384()}, {nistP521(), elliptic.P521()}} {
		if p.c.P.Cmp(p.std.Params().P) != 0 || p.c.N.Cmp(p.std.Params().N) != 0 {
			t.Errorf("%s disagrees with crypto/elliptic", p.c.Name)
		}
	}
	_ = big.NewInt
}

// TestVerifyRealFixtures verifies committed self-signed CSCA roots that Go's
// crypto/x509 cannot parse (Brainpool + explicit-parameter curves) plus an RSA
// one — a deterministic guard that the lenient parser + generic verifier work on
// real ICAO certs without needing the full master list.
func TestVerifyRealFixtures(t *testing.T) {
	cases := []struct {
		file    string
		wantKey string
	}{
		{"csca_brainpoolP256r1.der", "brainpoolP256r1"},
		{"csca_brainpoolP512r1.der", "brainpoolP512r1"},
		{"csca_p521_explicit.der", "P-521"},
		{"csca_rsa.der", "RSA"},
	}
	for _, tc := range cases {
		der, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		c, err := ParseCert(der)
		if err != nil {
			t.Errorf("%s: parse: %v", tc.file, err)
			continue
		}
		if keyName(c) != tc.wantKey {
			t.Errorf("%s: key = %s, want %s", tc.file, keyName(c), tc.wantKey)
		}
		if err := VerifySignedBy(c, c.PublicKey); err != nil {
			t.Errorf("%s: self-signature failed: %v", tc.file, err)
		}
	}
}
