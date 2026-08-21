package keeper

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"cosmossdk.io/collections"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/pki/types"
)

// Revocation is the emergency response to a compromised Document Signer, and it
// is the one piece of this module's state nothing else can reconstruct: a CSCA
// comes back from its certificate, but "we decided to stop trusting this signer"
// exists only in the revocation set. An export that dropped it would silently
// re-trust every certificate governance had revoked — which is worse than never
// having revoked it, because the operator believes it is still revoked.
func TestGenesisRoundTripsRevocations(t *testing.T) {
	k, ctx := newKeeperForTest(t)

	revoked := [][]byte{{0x01, 0x02}, {0x03, 0x04}}
	original := types.GenesisState{Params: types.DefaultParams(), RevokedDscs: revoked}
	require.NoError(t, original.Validate())
	require.NoError(t, k.InitGenesis(ctx, original))

	for _, id := range revoked {
		has, err := k.RevokedDscs.Has(ctx, id)
		require.NoError(t, err)
		require.True(t, has, "dsc %x should be revoked after import", id)
	}

	got, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, revoked, got.RevokedDscs)
}

func TestGenesisRejectsADuplicateRevocation(t *testing.T) {
	gs := types.GenesisState{
		Params:      types.DefaultParams(),
		RevokedDscs: [][]byte{{0x01}, {0x01}},
	}
	require.ErrorContains(t, gs.Validate(), "revoked twice")
}

// The trust store round-trips whole.
//
// It used to lose 170 of 536 certificates before an export was even involved:
// Cscas was keyed by SKI, which is one entry per signing *key*, and the master
// list carries several certificates per key — renewals and link certificates.
// Each overwrote the last at InitGenesis. Keyed by the certificate's own hash,
// with SKI demoted to an index, every certificate is its own record.
func TestGenesisKeepsEveryCertificateNotJustEveryKey(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))

	// Two certificates for one signing identity: same SKI, different bodies and
	// different subject DNs. This is the shape that used to collapse.
	shared := []byte{0xAB, 0xCD}
	a := fakeCsca(t, shared, "CN=Example CSCA 1", 1)
	b := fakeCsca(t, shared, "CN=Example CSCA 2", 2)

	for _, c := range [][]byte{a, b} {
		require.NoError(t, k.AddCscaDER(ctx, c))
	}

	var stored int
	require.NoError(t, k.Cscas.Walk(ctx, nil, func(_ []byte, _ types.Csca) (bool, error) {
		stored++
		return false, nil
	}))
	require.Equal(t, 2, stored, "two certificates under one key are two records")

	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.Len(t, gs.Cscas, 2, "export must carry both")

	// And a re-import keeps both, along with the indexes over them.
	fresh, freshCtx := newKeeperForTest(t)
	require.NoError(t, fresh.InitGenesis(freshCtx, *gs))

	var reBySKI int
	require.NoError(t, fresh.CscaBySKI.Walk(freshCtx, nil,
		func(_ collections.Pair[[]byte, []byte]) (bool, error) { reBySKI++; return false, nil }))
	require.Equal(t, 2, reBySKI, "both certificates must be reachable by the shared SKI")
}

// The AKI path has to return every certificate carrying the key, not one of
// them. They share a public key so any verifies the signature, but they differ
// in validity period, and the caller can only pick the one that was valid if it
// is handed all of them.
func TestIssuerLookupReturnsEverySiblingUnderTheKey(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))

	shared := []byte{0xAB, 0xCD}
	require.NoError(t, k.AddCscaDER(ctx, fakeCsca(t, shared, "CN=Example CSCA 1", 1)))
	require.NoError(t, k.AddCscaDER(ctx, fakeCsca(t, shared, "CN=Example CSCA 2", 2)))

	var n int
	rng := collections.NewPrefixedPairRange[[]byte, []byte](shared)
	require.NoError(t, k.CscaBySKI.Walk(ctx, rng,
		func(_ collections.Pair[[]byte, []byte]) (bool, error) { n++; return false, nil }))
	require.Equal(t, 2, n, "a DSC naming this AKI must see both certificates")
}

// fakeCsca builds a self-signed CA certificate with a chosen SubjectKeyId and
// subject name. Two calls with the same ski and different names produce what the
// real master list is full of: separate certificates for one signing identity.
func fakeCsca(t *testing.T, ski []byte, cn string, serial int64) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Country: []string{"XX"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		SubjectKeyId:          ski,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return der
}
