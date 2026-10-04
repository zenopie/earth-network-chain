package keeper

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

// makeRSADSC issues an RSA-2048 DSC signed by the given RSA CSCA.
func makeRSADSC(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey) ([]byte, *rsa.PublicKey) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(77),
		Subject:      pkix.Name{CommonName: "Test RSA DSC"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	return der, &key.PublicKey
}

// TestVerifyRSADSC checks that an RSA DSC is accepted and its commitment is
// Poseidon2 over (TagRSAExponent, e, modulus big-endian bytes), exactly what
// the RSA register circuits' poa_core::dsc_commitment_rsa produces.
func TestVerifyRSADSC(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	if err := k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}); err != nil {
		t.Fatalf("init genesis: %v", err)
	}

	dscDER, rsaPub := makeRSADSC(t, ca, caKey)
	pub, err := k.VerifyDsc(ctx, dscDER)
	if err != nil {
		t.Fatalf("VerifyDsc (RSA): %v", err)
	}
	// For RSA the canonical key is the modulus big-endian, which is what the
	// register circuits hash after the exponent.
	if !bytes.Equal(pub.CanonicalBytes(), rsaPub.N.Bytes()) {
		t.Fatal("VerifyDsc returned a key other than the RSA modulus")
	}
	// Every modulus size shares one tag: RSA keys already differ in length, and
	// the sponge separates lengths on its own.
	if tag, err := pub.CurveTagOf(); err != nil || tag != certs.TagRSAExponent {
		t.Fatalf("CurveTagOf = %v, %v; want %v", tag, err, certs.TagRSAExponent)
	}
	got, err := certs.DscCommitmentOf(pub)
	if err != nil {
		t.Fatal(err)
	}
	if want := certs.DscCommitmentRSA(uint64(rsaPub.E), rsaPub.N.Bytes()); !got.Equal(&want) {
		t.Fatal("RSA commitment does not absorb the exponent")
	}
	if other := certs.DscCommitmentRSA(3, rsaPub.N.Bytes()); got.Equal(&other) {
		t.Fatal("RSA commitment is the same for another exponent")
	}
}
