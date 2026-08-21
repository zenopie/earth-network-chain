package keeper

import (
	"context"
	"crypto/sha256"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

// cscaID is the storage key for a CSCA: its SubjectKeyIdentifier when present,
// else the SHA-256 of its canonical public key. A DSC's AuthorityKeyIdentifier
// points at the issuing CSCA's SKI, so this doubles as the AKI lookup key.
func cscaID(c *certs.Cert) []byte {
	if len(c.SKI) > 0 {
		return c.SKI
	}
	h := sha256.Sum256(c.PublicKey.CanonicalBytes())
	return h[:]
}

// certID identifies one certificate, as distinct from cscaID which identifies
// the signing key behind it. A CSCA that is renewed, or that issues a link
// certificate, produces several certificates under one key; they are separate
// records here and one entry each in CscaBySKI.
func certID(der []byte) []byte {
	h := sha256.Sum256(der)
	return h[:]
}

// dnKey is the CscaByDN index key for a distinguished name. Subject DNs are
// variable length and real ICAO CSCAs exceed the 255-byte ceiling collections
// imposes on a non-terminal bytes key, so the DN is hashed to a fixed size.
// Equality of DNs is preserved, which is all issuer matching needs.
func dnKey(dn []byte) []byte {
	h := sha256.Sum256(dn)
	return h[:]
}

// VerifyDsc checks that a DER-encoded Document Signer certificate is currently
// valid, chains to a CSCA in the trust store, and has not been revoked. It
// returns the DSC's canonical public-key bytes (ECDSA: x‖y, RSA: modulus
// big-endian) so callers can derive the commitment the register circuit exposes.
//
// This is the whole trust decision for a registration: the circuit proves the
// passport's SOD was signed by this key, and this proves the key is a genuine,
// still-trusted Document Signer.
func (k Keeper) VerifyDsc(ctx context.Context, der []byte) ([]byte, error) {
	dsc, err := certs.ParseCert(der)
	if err != nil {
		return nil, types.ErrInvalidCert.Wrap(err.Error())
	}
	// The DSC's own validity is enforced; its issuer's deliberately is not. See
	// Csca.not_after in pki.proto — an expired CSCA still signed genuine
	// passports that are still in their own validity period, and refusing them
	// would break verification for a whole country as its trust store aged.
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	if now.Before(dsc.NotBefore) || now.After(dsc.NotAfter) {
		return nil, types.ErrCertExpired
	}

	pub := dsc.PublicKey.CanonicalBytes()
	hash := sha256.Sum256(pub)
	if revoked, err := k.RevokedDscs.Has(ctx, hash[:]); err != nil {
		return nil, err
	} else if revoked {
		return nil, types.ErrDscRevoked
	}

	cands, err := k.issuerCandidates(ctx, dsc)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, types.ErrNoIssuerCsca
	}
	// First candidate whose key verifies the signature wins. Candidates sharing
	// an SKI share a public key, so in practice the first is decisive and the
	// rest are never reached; the list matters when the AKI and the issuer DN
	// point at genuinely different signing identities.
	for _, csca := range cands {
		if certs.VerifySignedBy(dsc, csca.PublicKey) == nil {
			return pub, nil
		}
	}
	return nil, types.ErrCertVerify
}

// RevokeDsc marks a Document Signer's public key as untrusted, by the same
// sha256(canonical pubkey) identity VerifyDsc checks. Registrations already
// recorded are unaffected; because each registration names its DSC publicly,
// they can be enumerated and dealt with separately.
func (k Keeper) RevokeDsc(ctx context.Context, pubkey []byte) error {
	hash := sha256.Sum256(pubkey)
	return k.RevokedDscs.Set(ctx, hash[:])
}

// AddCscaDER parses, records, and indexes a trusted CSCA certificate.
func (k Keeper) AddCscaDER(ctx context.Context, der []byte) error {
	c, err := certs.ParseCert(der)
	if err != nil {
		return types.ErrInvalidCert.Wrap(err.Error())
	}
	id := certID(der)
	csca := types.Csca{
		CertificateDer: der,
		SubjectKeyId:   c.SKI,
		SubjectDn:      c.SubjectRaw,
		NotAfter:       c.NotAfter.Unix(),
	}
	if err := k.Cscas.Set(ctx, id, csca); err != nil {
		return err
	}
	// Both indexes point at the certificate, not at its key: a DSC may name its
	// issuer by AKI or by issuer DN, and either has to reach the certificate that
	// was actually named rather than an arbitrary sibling sharing the key.
	if err := k.CscaBySKI.Set(ctx, collections.Join(cscaID(c), id)); err != nil {
		return err
	}
	return k.CscaByDN.Set(ctx, collections.Join(dnKey(c.SubjectRaw), id))
}

// issuerCandidates returns parsed CSCAs that could have issued dsc: every
// certificate whose SKI matches the DSC's AKI, plus any whose subject DN equals
// the DSC's issuer DN.
//
// Both lookups go through an index rather than a direct Get, because one key and
// one DN can each name several certificates — renewals and link certificates for
// the same signing identity. They share a public key, so for verification any of
// them does; the reason to return all is that the store now holds all, and a
// lookup that silently picked one would put the old collapse back at a different
// layer.
func (k Keeper) issuerCandidates(ctx context.Context, dsc *certs.Cert) ([]*certs.Cert, error) {
	var out []*certs.Cert
	seen := map[string]bool{}
	add := func(id []byte) {
		if seen[string(id)] {
			return
		}
		seen[string(id)] = true
		if csca, err := k.Cscas.Get(ctx, id); err == nil {
			if pc, err := certs.ParseCert(csca.CertificateDer); err == nil {
				out = append(out, pc)
			}
		}
	}

	collect := func(idx collections.KeySet[collections.Pair[[]byte, []byte]], prefix []byte) error {
		rng := collections.NewPrefixedPairRange[[]byte, []byte](prefix)
		return idx.Walk(ctx, rng, func(key collections.Pair[[]byte, []byte]) (bool, error) {
			add(key.K2())
			return false, nil
		})
	}

	if len(dsc.AKI) > 0 {
		if err := collect(k.CscaBySKI, dsc.AKI); err != nil {
			return nil, err
		}
	}
	if err := collect(k.CscaByDN, dnKey(dsc.IssuerRaw)); err != nil {
		return nil, err
	}
	return out, nil
}
