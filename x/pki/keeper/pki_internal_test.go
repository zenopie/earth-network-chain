package keeper

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/pki/types"
)

func newKeeperForTest(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName))
	return k, ctx
}

func makeCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CSCA", Country: []string{"XX"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		SubjectKeyId:          []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func makeECDSC(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey) ([]byte, *ecdsa.PublicKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "Test DSC"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	return der, &key.PublicKey
}

// TestVerifyDscFlow drives the module's trust decision: seed a CSCA, then check
// that a DSC it issued verifies, that revoking the signer withdraws that trust,
// and that a DSC from an unknown CA is refused.
func TestVerifyDscFlow(t *testing.T) {
	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())

	ca, caKey := makeCA(t)
	if err := k.InitGenesis(ctx, types.GenesisState{
		Params: types.DefaultParams(),
		Cscas:  []types.Csca{{CertificateDer: ca.Raw}},
	}); err != nil {
		t.Fatalf("init genesis: %v", err)
	}

	dscDER, ecPub := makeECDSC(t, ca, caKey)

	pub, err := k.VerifyDsc(ctx, dscDER)
	if err != nil {
		t.Fatalf("VerifyDsc: %v", err)
	}
	// The returned key is the commitment preimage: x‖y for ECDSA.
	var xb, yb [32]byte
	ecPub.X.FillBytes(xb[:])
	ecPub.Y.FillBytes(yb[:])
	if !bytes.Equal(pub, append(xb[:], yb[:]...)) {
		t.Fatal("VerifyDsc returned a different canonical public key")
	}

	// A DSC from an untrusted CA has no issuer in the store.
	otherCA, otherKey := makeCA(t)
	rogueDER, _ := makeECDSC(t, otherCA, otherKey)
	if _, err := k.VerifyDsc(ctx, rogueDER); err == nil {
		t.Fatal("expected rejection of a DSC from an untrusted CSCA")
	}

	// Revocation withdraws trust from a previously good signer.
	if err := k.RevokeDsc(ctx, pub); err != nil {
		t.Fatalf("RevokeDsc: %v", err)
	}
	if _, err := k.VerifyDsc(ctx, dscDER); err == nil {
		t.Fatal("expected rejection after revocation")
	}
}
