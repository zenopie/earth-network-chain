package certs

import (
	"bytes"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalBytesFromRealCerts checks the step between a parsed certificate
// and the DSC commitment: that CanonicalBytes returns exactly the key encoding
// the register circuits hash — x‖y zero-padded to the curve's coordinate width
// for ECDSA, and the big-endian modulus for RSA.
//
// The padding is the part that can silently go wrong. A coordinate with leading
// zero bytes has a shorter big.Int representation, so an implementation that
// forgot to left-pad would produce a shorter byte string, a different Poseidon2
// commitment, and registrations that fail only for the unlucky keys on one
// curve. Real Brainpool certificates are used because crypto/x509 cannot encode
// them, so they cannot be covered by a generated fixture.
func TestCanonicalBytesFromRealCerts(t *testing.T) {
	cases := []struct {
		file  string
		curve string
	}{
		{"csca_brainpoolP256r1.der", "brainpoolP256r1"},
		{"csca_brainpoolP512r1.der", "brainpoolP512r1"},
		{"csca_p521_explicit.der", "P-521"},
		{"csca_rsa.der", "RSA"},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			der, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Skip(err)
			}
			c, err := ParseCert(der)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := c.PublicKey.CanonicalBytes()

			if c.PublicKey.IsRSA {
				if want := c.PublicKey.RSAModulus.Bytes(); !bytes.Equal(got, want) {
					t.Fatalf("RSA canonical bytes are not the modulus (%d vs %d bytes)", len(got), len(want))
				}
				return
			}

			if c.PublicKey.Curve.Name != tc.curve {
				t.Fatalf("curve = %s, want %s", c.PublicKey.Curve.Name, tc.curve)
			}
			width := c.PublicKey.Curve.byteLen
			if len(got) != 2*width {
				t.Fatalf("canonical length = %d, want %d (x‖y at %d bytes each)", len(got), 2*width, width)
			}
			// Each half must be the coordinate, left-padded — not merely
			// "contains" it.
			var wantX, wantY = make([]byte, width), make([]byte, width)
			c.PublicKey.X.FillBytes(wantX)
			c.PublicKey.Y.FillBytes(wantY)
			if !bytes.Equal(got[:width], wantX) || !bytes.Equal(got[width:], wantY) {
				t.Fatal("canonical bytes are not x‖y left-padded to the coordinate width")
			}
			// And the halves must round-trip back to the same integers.
			if new(big.Int).SetBytes(got[:width]).Cmp(c.PublicKey.X) != 0 ||
				new(big.Int).SetBytes(got[width:]).Cmp(c.PublicKey.Y) != 0 {
				t.Fatal("canonical bytes do not round-trip to the key coordinates")
			}
		})
	}
}
