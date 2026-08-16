package certs

import (
	"os"
	"testing"
)

// TestParseMasterList extracts CSCA certs from a real ICAO master list. Point
// MASTERLIST_ML at csca_masterlist/allowlist.ml. Skips if unset.
func TestParseMasterList(t *testing.T) {
	path := os.Getenv("MASTERLIST_ML")
	if path == "" {
		t.Skip("set MASTERLIST_ML to an ICAO allowlist.ml")
	}
	der, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ders, err := ParseMasterList(der)
	if err != nil {
		t.Fatal(err)
	}
	if len(ders) < 100 {
		t.Fatalf("expected many CSCAs, got %d", len(ders))
	}
	parsed := 0
	for _, d := range ders {
		if _, err := ParseCert(d); err == nil {
			parsed++
		}
	}
	t.Logf("master list: %d certs, %d parse with our lenient parser", len(ders), parsed)
	if parsed != len(ders) {
		t.Errorf("only %d/%d certs parsed", parsed, len(ders))
	}
}
