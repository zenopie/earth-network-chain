package keeper

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

// Regression tests using certificates taken from the real ICAO CSCA master list.
// The synthetic certificates the other tests generate are all well-behaved; real
// ones are not, and genesis seeding panicked on them.

// TestAddCscaLongSubjectDN covers a real CSCA whose subject DN is 297 bytes.
//
// CscaByDN is a Pair key and collections length-prefixes a non-terminal bytes
// key with a single byte, so a raw DN over 255 bytes cannot be encoded: seeding
// this certificate used to abort InitGenesis with "bytes key non terminal size
// cannot exceed: 255". The index therefore keys on a hash of the DN.
func TestAddCscaLongSubjectDN(t *testing.T) {
	der, err := os.ReadFile("testdata/csca_long_dn.der")
	if err != nil {
		t.Skip("testdata/csca_long_dn.der not present")
	}
	parsed, err := certs.ParseCert(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.SubjectRaw) <= 255 {
		t.Fatalf("fixture no longer exercises the bug: subject DN is %d bytes", len(parsed.SubjectRaw))
	}

	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Now())
	if err := k.InitGenesis(ctx, types.GenesisState{
		Params: types.NewParams(),
		Cscas:  []types.Csca{{CertificateDer: der}},
	}); err != nil {
		t.Fatalf("InitGenesis with a %d-byte subject DN: %v", len(parsed.SubjectRaw), err)
	}

	// The DN index must still resolve this CSCA as an issuer candidate.
	cands, _, _, err := k.issuerCandidates(ctx, &certs.Cert{IssuerRaw: parsed.SubjectRaw})
	if err != nil {
		t.Fatalf("issuerCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("long-DN CSCA not resolvable by subject DN")
	}
}

// TestRealCscaIsNotADsc runs a real ICAO root through VerifyDsc. It used to be
// accepted — the store's own key verifies it — which made every country's root
// usable as a Document Signer. Its issuer is still found and its signature
// still checks; what refuses it is that it is a CA.
func TestRealCscaIsNotADsc(t *testing.T) {
	der, err := os.ReadFile("testdata/csca_self_signed.der")
	if err != nil {
		t.Skip("testdata/csca_self_signed.der not present")
	}
	parsed, err := certs.ParseCert(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.IsCA || !parsed.CertSign {
		t.Fatalf("real CSCA parsed as IsCA=%v CertSign=%v; want both", parsed.IsCA, parsed.CertSign)
	}

	k, ctx := newKeeperForTest(t)
	ctx = ctx.WithBlockTime(parsed.NotBefore.Add(time.Hour))
	if err := k.InitGenesis(ctx, types.GenesisState{
		Params: types.NewParams(),
		Cscas:  []types.Csca{{CertificateDer: der}},
	}); err != nil {
		t.Fatalf("InitGenesis: %v", err)
	}

	if _, err := k.VerifyDsc(ctx, der); !errors.Is(err, types.ErrNotDsc) {
		t.Fatalf("VerifyDsc(real CSCA) = %v, want ErrNotDsc", err)
	}
}
