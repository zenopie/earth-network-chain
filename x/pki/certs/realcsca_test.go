package certs

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestParseAndVerifyRealCSCAs runs the lenient parser + generic verifier over the
// real ICAO CSCA master list — the certs Go's crypto/x509 rejects (Brainpool and
// explicit-parameter curves). Each cert is verified against its actual issuer
// (self for roots, the SKI==AKI sibling for rollover/link certs), proving the
// parser + big.Int ECDSA (all curves) + RSA path works on real-world certs.
//
// Point CSCA_DER_DIR at extracted CSCA *.der files (from the backend's
// tools/extract_csca on csca/masterlist/allowlist.ml). Skips if unset.
func TestParseAndVerifyRealCSCAs(t *testing.T) {
	dir := os.Getenv("CSCA_DER_DIR")
	if dir == "" {
		t.Skip("set CSCA_DER_DIR to the extracted CSCA .der directory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.der"))
	if err != nil || len(files) == 0 {
		t.Skipf("no .der files in %s", dir)
	}

	var all []*Cert
	bySubjectDN := map[string][]*Cert{}
	byKey := map[string]int{}
	verifiedByKey := map[string]int{}
	parsed := 0
	for _, f := range files {
		der, _ := os.ReadFile(f)
		c, err := ParseCert(der)
		if err != nil {
			t.Errorf("parse %s: %v", filepath.Base(f), err)
			continue
		}
		parsed++
		all = append(all, c)
		bySubjectDN[string(c.SubjectRaw)] = append(bySubjectDN[string(c.SubjectRaw)], c)
		byKey[keyName(c)]++
	}
	t.Logf("parsed %d/%d", parsed, len(files))

	// Verify each cert against a candidate issuer: any cert whose subject DN equals
	// this cert's issuer DN (this includes self for self-signed roots, and covers
	// rollover/link certs that omit an AKI). Accept if any candidate's key verifies.
	verified, parentMissing := 0, 0
	for _, c := range all {
		candidates := bySubjectDN[string(c.IssuerRaw)]
		if len(candidates) == 0 {
			parentMissing++
			continue
		}
		ok := false
		var lastErr error
		for _, cand := range candidates {
			if err := VerifySignedBy(c, cand.PublicKey); err == nil {
				ok = true
				break
			} else {
				lastErr = err
			}
		}
		if !ok {
			t.Errorf("verify %s cert against %d issuer candidate(s): %v", keyName(c), len(candidates), lastErr)
			continue
		}
		verified++
		verifiedByKey[keyName(c)]++
	}

	t.Logf("verified %d, parent-missing %d", verified, parentMissing)
	var keys []string
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-16s parsed %d, verified %d", k, byKey[k], verifiedByKey[k])
	}
	// The whole point: the curves stdlib can't do must verify with our generic path.
	for _, want := range []string{"brainpoolP256r1", "brainpoolP384r1", "brainpoolP512r1", "P-256", "P-384", "P-521", "RSA"} {
		if verifiedByKey[want] == 0 {
			t.Errorf("expected >=1 verified %s cert, got 0", want)
		}
	}
}

func keyName(c *Cert) string {
	if c.PublicKey.IsRSA {
		return "RSA"
	}
	return c.PublicKey.Curve.Name
}
