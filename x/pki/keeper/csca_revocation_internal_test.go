package keeper

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

// reissue produces a second certificate for a CSCA that already exists: same
// signing key, same SKI, different serial and so different DER. This is what a
// renewal or a link certificate looks like in the master list, and the store
// keeps them as separate records because they are separate certificates.
//
// It is also the shape that decides whether CSCA revocation works at all. Both
// records verify a child signature, because verifying is a property of the key
// and not of the certificate carrying it.
func reissue(t *testing.T, ca *x509.Certificate, key *rsa.PrivateKey, serial int64) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "Test CSCA", Country: []string{"XX"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(20 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		SubjectKeyId:          ca.SubjectKeyId,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	out, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return out
}

// TestRevokeCscaStopsNewDscs is the plain case: a CSCA that verified a Document
// Signer stops doing so once governance revokes it, and says why.
func TestRevokeCscaStopsNewDscs(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}))

	dscDER, _ := makeECDSC(t, ca, caKey)
	_, err := k.VerifyDsc(ctx, dscDER)
	require.NoError(t, err, "DSC should verify before the CSCA is revoked")

	require.NoError(t, k.RevokeCsca(ctx, canonicalPub(t, ca)))

	_, err = k.VerifyDsc(ctx, dscDER)
	require.True(t, errors.Is(err, types.ErrCscaRevoked),
		"want ErrCscaRevoked, got %v", err)
}

// TestRevokeCscaCoversSiblingCertificates is the reason revocation is keyed by
// signing key rather than by certificate.
//
// Revoking the certificate that was handed in, and only that one, would leave
// every renewal and link certificate sharing its key still verifying — the
// revocation would look like it worked and change nothing. A country's presence
// in the master list is a key, not a file.
func TestRevokeCscaCoversSiblingCertificates(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	renewal := reissue(t, ca, caKey, 99)
	require.NotEqual(t, ca.Raw, renewal.Raw, "renewal must be a distinct certificate")

	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas: []types.Csca{
			{CertificateDer: ca.Raw},
			{CertificateDer: renewal.Raw},
		},
	}))

	dscDER, _ := makeECDSC(t, ca, caKey)
	_, err := k.VerifyDsc(ctx, dscDER)
	require.NoError(t, err)

	// Hand in the renewal, not the certificate that signed the DSC.
	require.NoError(t, k.RevokeCsca(ctx, canonicalPub(t, renewal)))

	_, err = k.VerifyDsc(ctx, dscDER)
	require.True(t, errors.Is(err, types.ErrCscaRevoked),
		"revoking one certificate must cover its siblings; got %v", err)
}

// TestRevokeCscaLeavesOtherIssuersAlone: revocation is scoped to one key, not to
// the trust store.
func TestRevokeCscaLeavesOtherIssuersAlone(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	badCA, badKey := makeCA(t)
	goodCA, goodKey := makeCA(t)
	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas: []types.Csca{
			{CertificateDer: badCA.Raw},
			{CertificateDer: goodCA.Raw},
		},
	}))

	badDSC, _ := makeECDSC(t, badCA, badKey)
	goodDSC, _ := makeECDSC(t, goodCA, goodKey)

	require.NoError(t, k.RevokeCsca(ctx, canonicalPub(t, badCA)))

	_, err := k.VerifyDsc(ctx, badDSC)
	require.Error(t, err)
	_, err = k.VerifyDsc(ctx, goodDSC)
	require.NoError(t, err, "an unrelated CSCA must keep verifying")
}

// TestAddCscaClearsRevocation covers the only route back to trusting a CSCA.
// There is no un-revoke message; re-adding the certificate is it, and that is
// what keeps the store from holding certificates that silently verify nothing.
func TestAddCscaClearsRevocation(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}))
	dscDER, _ := makeECDSC(t, ca, caKey)

	require.NoError(t, k.RevokeCsca(ctx, canonicalPub(t, ca)))
	_, err := k.VerifyDsc(ctx, dscDER)
	require.Error(t, err)

	require.NoError(t, k.AddCscaDER(ctx, ca.Raw))
	_, err = k.VerifyDsc(ctx, dscDER)
	require.NoError(t, err, "re-adding the certificate should re-trust its key")
}

// TestRevokedCscasSurviveGenesisRoundTrip guards the ordering trap in
// InitGenesis. The replayed CSCAs each clear their own revocation on the way in,
// so restoring the revoked set before that loop instead of after would hand a
// restarted chain exactly the trust store governance had thrown out — and it
// would do it silently, with a correct-looking export.
func TestRevokedCscasSurviveGenesisRoundTrip(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}))
	dscDER, _ := makeECDSC(t, ca, caKey)
	require.NoError(t, k.RevokeCsca(ctx, canonicalPub(t, ca)))

	exported, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.Len(t, exported.RevokedCscas, 1, "revocation must reach the export")
	require.Len(t, exported.Cscas, 1, "the certificate itself is still carried")

	k2, ctx2 := newKeeperForTest(t)
	ctx2 = ctx2.WithBlockTime(time.Now())
	require.NoError(t, k2.InitGenesis(ctx2, *exported))

	_, err = k2.VerifyDsc(ctx2, dscDER)
	require.True(t, errors.Is(err, types.ErrCscaRevoked),
		"a restarted chain must not re-trust a revoked CSCA; got %v", err)
}

// TestRevokeCscaRejectsNonAuthority: the message is governance-gated for the
// same reason RevokeDsc is — an erroneous revocation locks out every holder
// whose passport that country issued.
func TestRevokeCscaRejectsNonAuthority(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	require.NoError(t, k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}))
	dscDER, _ := makeECDSC(t, ca, caKey)

	notAuthority, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).
		BytesToString(authtypes.NewModuleAddress("not-gov"))
	require.NoError(t, err)

	ms := NewMsgServerImpl(k)
	_, err = ms.RevokeCsca(ctx, &types.MsgRevokeCsca{
		Authority:      notAuthority,
		CertificateDer: ca.Raw,
	})
	require.Error(t, err)

	_, err = k.VerifyDsc(ctx, dscDER)
	require.NoError(t, err, "a refused revocation must not have taken effect")
}

// canonicalPub is the identity RevokeCsca takes: the certificate's public key in
// the module's canonical encoding, which is what the revoked set is keyed by.
func canonicalPub(t *testing.T, cert *x509.Certificate) []byte {
	t.Helper()
	c, err := certs.ParseCert(cert.Raw)
	require.NoError(t, err)
	return c.PublicKey.CanonicalBytes()
}
