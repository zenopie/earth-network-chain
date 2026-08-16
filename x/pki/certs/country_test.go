package certs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCountryFromRealCscas checks country extraction against real ICAO
// certificates — the DN encodings that crypto/x509 refuses are exactly the ones
// this has to survive.
func TestCountryFromRealCscas(t *testing.T) {
	// Ground truth from `openssl x509 -subject` on each fixture.
	cases := map[string]string{
		"csca_brainpoolP256r1.der": "LV", // CSCA Latvia
		"csca_brainpoolP512r1.der": "ET", // Ethiopia CSCA
		"csca_rsa.der":             "IN", // NIC sub-CA for ePassport-India
		"csca_p521_explicit.der":   "EG", // CSCA EG Sec
	}
	for file, want := range cases {
		der, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Skip(err)
		}
		c, err := ParseCert(der)
		if err != nil {
			t.Fatalf("%s: parse: %v", file, err)
		}
		if got := c.Country(); got != want {
			t.Errorf("%s: country = %q, want %q", file, got, want)
		}
	}
}
