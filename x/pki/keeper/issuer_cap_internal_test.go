package keeper

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/earth-network/earth/x/pki/types"
)

// The candidate cap must never be the reason a genuine passport fails.
//
// It exists to bound what one VerifyDsc call can cost, and a bound that stops a
// country registering is a worse failure than the cost it was imposed to
// prevent. Two properties keep it off the path of real certificates, and both
// are pinned here: the cap truncates rather than refusing, and the AKI lookup
// runs first so a DSC that names its issuer finds it before the ceiling is
// anywhere in sight.
func TestIssuerCapDoesNotBlockAVerifiableDsc(t *testing.T) {
	k, ctx := newKeeperForTest(t)

	// The real issuer, and its DSC.
	ca, caKey := makeCA(t)
	dscDER, _ := makeECDSC(t, ca, caKey)
	if err := k.AddCscaDER(ctx, ca.Raw); err != nil {
		t.Fatalf("AddCscaDER: %v", err)
	}

	// Bury it: far more certificates than the cap, all sharing the issuer DN,
	// none of which can verify this DSC. Without truncation-and-ordering this
	// is exactly the shape that would lock the country out.
	for i := 0; i < types.MaxIssuerCandidates*2; i++ {
		if err := k.AddCscaDER(ctx, sameDnDecoy(t, ca.Subject, i)); err != nil {
			t.Fatalf("AddCscaDER decoy %d: %v", i, err)
		}
	}

	pub, err := k.VerifyDsc(ctx, dscDER)
	if err != nil {
		t.Fatalf("a DSC with a valid AKI must still verify past the cap: %v", err)
	}
	if pub == nil || len(pub.CanonicalBytes()) == 0 {
		t.Fatal("VerifyDsc returned an empty canonical public key")
	}
}

// And when the cap really did hide the answer, the error says so. "No issuer
// found" would send an operator looking for a CSCA that is present.
func TestIssuerCapReportsTruncation(t *testing.T) {
	k, ctx := newKeeperForTest(t)

	ca, caKey := makeCA(t)
	dscDER, _ := makeECDSC(t, ca, caKey)

	// The genuine issuer is NOT added, so nothing can verify the DSC — but its
	// DN is crowded past the cap, so the chain cannot claim it looked everywhere.
	for i := 0; i < types.MaxIssuerCandidates*2; i++ {
		if err := k.AddCscaDER(ctx, sameDnDecoy(t, ca.Subject, i)); err != nil {
			t.Fatalf("AddCscaDER decoy %d: %v", i, err)
		}
	}

	_, err := k.VerifyDsc(ctx, dscDER)
	if err == nil {
		t.Fatal("expected verification to fail")
	}
	if !types.ErrTooManyIssuers.Is(err) {
		t.Fatalf("want ErrTooManyIssuers so the operator knows to prune, got %v", err)
	}
}

// sameDnDecoy makes a self-signed CSCA sharing `subject` — so it lands under
// the same CscaByDN key — with its own key, so it verifies nothing.
func sameDnDecoy(t *testing.T, subject pkix.Name, i int) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("decoy key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(int64(1000 + i)),
		Subject:               subject,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		// A distinct SKI, so these never match the DSC's AKI and only ever
		// arrive through the DN sweep — which is the half the cap can cut.
		SubjectKeyId: []byte{9, 9, byte(i >> 8), byte(i)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("decoy cert: %v", err)
	}
	return der
}
