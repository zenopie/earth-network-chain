package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func makeCSCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CSCA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// makeDSC issues an ECDSA-P256 DSC signed by the given CSCA, valid over [nb,na].
func makeDSC(t *testing.T, csca *x509.Certificate, caKey *rsa.PrivateKey, nb, na time.Time) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "Test DSC"},
		NotBefore:    nb,
		NotAfter:     na,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, csca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func TestVerifyDSC(t *testing.T) {
	csca, caKey := makeCSCA(t)
	now := time.Now()
	dscDER, dscKey := makeDSC(t, csca, caKey, now.Add(-time.Hour), now.Add(365*24*time.Hour))

	// Happy path: valid DSC signed by the CSCA.
	dsc, err := VerifyDSC(dscDER, csca, now)
	if err != nil {
		t.Fatalf("valid DSC rejected: %v", err)
	}
	if dsc.KeyType != KeyECDSA || len(dsc.Pubkey) != 64 {
		t.Fatalf("unexpected key: type=%s len=%d", dsc.KeyType, len(dsc.Pubkey))
	}
	// Pubkey must be x‖y (32+32), matching the ECDSA public key.
	var wantX, wantY [32]byte
	dscKey.PublicKey.X.FillBytes(wantX[:])
	dscKey.PublicKey.Y.FillBytes(wantY[:])
	if string(dsc.Pubkey[:32]) != string(wantX[:]) || string(dsc.Pubkey[32:]) != string(wantY[:]) {
		t.Fatal("canonical pubkey != x‖y")
	}

	// Wrong CSCA -> rejected.
	other, _ := makeCSCA(t)
	if _, err := VerifyDSC(dscDER, other, now); err == nil {
		t.Fatal("expected rejection: DSC not signed by this CSCA")
	}

	// Expired DSC -> rejected.
	expDER, _ := makeDSC(t, csca, caKey, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	if _, err := VerifyDSC(expDER, csca, now); err == nil {
		t.Fatal("expected rejection: expired DSC")
	}

	// Not-yet-valid DSC -> rejected.
	futDER, _ := makeDSC(t, csca, caKey, now.Add(24*time.Hour), now.Add(48*time.Hour))
	if _, err := VerifyDSC(futDER, csca, now); err == nil {
		t.Fatal("expected rejection: DSC not yet valid")
	}
}
