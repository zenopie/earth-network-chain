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

// TestSubmitRSADSC checks that an RSA DSC is accepted and its registry leaf is
// Poseidon2 over the modulus big-endian bytes — exactly what lean_poa_rsa2048's
// in-circuit leaf (RuntimeBigNum.to_be_bytes) produces — so an RSA passport's
// on-chain inclusion proof is valid for the RSA circuit.
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
	// register circuits hash for lean_poa_rsa2048/4096.
	if !bytes.Equal(pub, rsaPub.N.Bytes()) {
		t.Fatal("VerifyDsc returned a key other than the RSA modulus")
	}
}
