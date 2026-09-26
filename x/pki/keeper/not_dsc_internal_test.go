package keeper

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/earth-network/earth/x/pki/types"
)

// TestIssuerCertificatesAreNotDscs: every certificate a trusted CSCA key
// verifies used to pass as a Document Signer, the CSCA's own self-signed root
// and its link certificates included.
func TestIssuerCertificatesAreNotDscs(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	if err := k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}); err != nil {
		t.Fatalf("init genesis: %v", err)
	}

	issued := func(tmpl *x509.Certificate) []byte {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl.SerialNumber = big.NewInt(7)
		tmpl.NotBefore = time.Now().Add(-time.Hour)
		tmpl.NotAfter = time.Now().Add(24 * time.Hour)
		tmpl.AuthorityKeyId = ca.SubjectKeyId
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return der
	}

	cases := map[string][]byte{
		// The trust store's own root, presented back as a signer.
		"self-signed CSCA": ca.Raw,
		"sub-CA": issued(&x509.Certificate{
			Subject: pkix.Name{CommonName: "Sub CA"}, IsCA: true, BasicConstraintsValid: true,
		}),
		"certificate-signing key": issued(&x509.Certificate{
			Subject: pkix.Name{CommonName: "Signs certs"}, KeyUsage: x509.KeyUsageCertSign,
		}),
	}
	for name, der := range cases {
		if _, err := k.VerifyDsc(ctx, der); !errors.Is(err, types.ErrNotDsc) {
			t.Errorf("%s: VerifyDsc = %v, want ErrNotDsc", name, err)
		}
	}

	// A real Document Signer still verifies.
	good := issued(&x509.Certificate{
		Subject: pkix.Name{CommonName: "DSC"}, KeyUsage: x509.KeyUsageDigitalSignature,
	})
	if _, err := k.VerifyDsc(ctx, good); err != nil {
		t.Fatalf("genuine DSC: %v", err)
	}
}
