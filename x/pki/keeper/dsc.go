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
//
// The work it does is bounded, which matters because x/personhood charges one
// flat DscVerificationGas for the whole call and that number is only meaningful
// against a stated ceiling. The two inputs a submitter could otherwise use to
// set the cost are capped: the declared key size by certs.MaxPublicKeyBytes,
// enforced in the parser, and the number of issuer certificates tried by
// types.MaxIssuerCandidates, enforced below. Worst case is therefore
// MaxIssuerCandidates signature verifications over a key of at most
// MaxPublicKeyBytes; move either constant and revisit the gas with it.
func (k Keeper) VerifyDsc(ctx context.Context, der []byte) (*certs.PublicKey, error) {
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

	cands, sawRevoked, truncated, err := k.issuerCandidates(ctx, dsc)
	if err != nil {
		return nil, err
	}
	// First candidate whose key verifies the signature wins. Candidates sharing
	// an SKI share a public key, so in practice the first is decisive and the
	// rest are never reached; the list matters when the AKI and the issuer DN
	// point at genuinely different signing identities.
	for _, csca := range cands {
		if certs.VerifySignedBy(dsc, csca.PublicKey) == nil {
			// The parsed key rather than its bytes: the DSC commitment now needs
			// the curve as well as the coordinates, and the bytes alone cannot
			// say which curve produced them — which is the whole point of the
			// tag. See certs.DscCommitment.
			return dsc.PublicKey, nil
		}
	}
	// A revoked issuer is the more specific answer than "nothing here verified
	// it", and the one an operator needs to see: it says the chain refused a
	// certificate it can still verify, rather than one it cannot place. Checked
	// after the loop so a DSC that a *different*, still-trusted issuer verifies
	// is unaffected by a revocation elsewhere in the candidate set.
	if sawRevoked {
		return nil, types.ErrCscaRevoked
	}
	// Nothing verified it AND the candidate list was cut short, so the real
	// issuer may be one of the certificates never examined. Said plainly,
	// because the fix is to prune the trust store and "no issuer found" would
	// send an operator looking for a missing CSCA that is in fact present.
	if truncated {
		return nil, types.ErrTooManyIssuers.Wrapf(
			"stopped after examining %d candidate issuers and none verified this DSC",
			types.MaxIssuerCandidates)
	}
	if len(cands) == 0 {
		return nil, types.ErrNoIssuerCsca
	}
	return nil, types.ErrCertVerify
}

// RevokeDsc marks a Document Signer's public key as untrusted.
//
// The signer is recorded under both identities it has on this chain: the
// sha256(canonical pubkey) that VerifyDsc checks when a certificate is
// presented, and the Poseidon2 commitment that the register circuit exposes and
// that every Registration stores. Only the first stops new registrations; the
// second is what lets registrations already on the books be recognised as
// coming from a signer no longer trusted, which is the difference between
// revocation that stops the bleeding and revocation that only closes the door.
func (k Keeper) RevokeDsc(ctx context.Context, pk *certs.PublicKey) error {
	hash := sha256.Sum256(pk.CanonicalBytes())
	if err := k.RevokedDscs.Set(ctx, hash[:]); err != nil {
		return err
	}
	// Takes the parsed key, not its bytes, so the commitment it computes is the
	// one the registrations actually carry. Given only bytes this would have to
	// guess the curve, and a wrong guess is the quiet failure: the sha256 half
	// still closes the door to new registrations while the commitment half
	// matches nothing, so existing registrations under the revoked signer are
	// never retired.
	c, err := certs.DscCommitmentOf(pk)
	if err != nil {
		return err
	}
	commitment := c.Bytes()
	if err := k.RevokedDscCommitments.Set(ctx, commitment[:]); err != nil {
		return err
	}
	// Tell whoever holds records made under this signer, so retiring them starts
	// now rather than on a second governance vote a week from now.
	for _, l := range *k.revocationListeners {
		if err := l.OnDscRevoked(ctx, commitment[:]); err != nil {
			return err
		}
	}
	return nil
}

// RegisterRevocationListener attaches a listener. Called once, from module
// wiring, by whichever module holds records tied to a Document Signer.
func (k Keeper) RegisterRevocationListener(l types.DscRevocationListener) {
	*k.revocationListeners = append(*k.revocationListeners, l)
}

// IsCommitmentRevoked reports whether the Document Signer behind a Poseidon2
// commitment has been revoked. It is how x/personhood asks about a registration
// it has already recorded, which knows its signer only by that commitment.
func (k Keeper) IsCommitmentRevoked(ctx context.Context, commitment []byte) (bool, error) {
	if len(commitment) == 0 {
		return false, nil
	}
	return k.RevokedDscCommitments.Has(ctx, commitment)
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
	if err := k.CscaByDN.Set(ctx, collections.Join(dnKey(c.SubjectRaw), id)); err != nil {
		return err
	}
	// Adding a certificate re-trusts its signing key. This is the only way back
	// from RevokeCsca — there is no un-revoke message — and it keeps "a
	// certificate in this store is one the chain will verify against" true.
	// Without it the store could hold a certificate that silently verifies
	// nothing, which is the state that wastes an afternoon to diagnose.
	//
	// InitGenesis depends on this: it replays every CSCA through here, so the
	// revoked set has to be restored *after* that loop rather than before.
	keyHash := sha256.Sum256(c.PublicKey.CanonicalBytes())
	return k.RevokedCscas.Remove(ctx, keyHash[:])
}

// RevokeCsca marks a CSCA's signing key as untrusted, so no Document Signer
// chaining to it verifies from here on.
//
// Keyed by the signing key rather than the certificate presented: a CSCA
// renewal or link certificate is a separate record sharing one public key, and
// any of them verifies a child signature, so revoking one certificate would
// leave its siblings doing exactly what the revocation meant to stop.
//
// Deliberately prospective, and deliberately quieter than RevokeDsc. No
// listener fires and no purge starts, so registrations already made under this
// CSCA keep their weight and keep claiming. Retiring those is the per-DSC path:
// RevokeDsc names a signer, and the purge it triggers unwinds the registrations
// that signer produced. Left alone they lapse on their own within one
// registration_validity_seconds, because renewing a registration re-verifies
// the Document Signer and that verification now fails here.
func (k Keeper) RevokeCsca(ctx context.Context, pubkey []byte) error {
	hash := sha256.Sum256(pubkey)
	return k.RevokedCscas.Set(ctx, hash[:])
}

// IsCscaRevoked reports whether a CSCA signing key has been revoked.
func (k Keeper) IsCscaRevoked(ctx context.Context, pubkey []byte) (bool, error) {
	hash := sha256.Sum256(pubkey)
	return k.RevokedCscas.Has(ctx, hash[:])
}

// issuerCandidates returns parsed CSCAs that could have issued dsc: every
// certificate whose SKI matches the DSC's AKI, plus any whose subject DN equals
// the DSC's issuer DN. Revoked issuers are left out, and the second return
// reports whether any were, so the caller can say "revoked" rather than the
// vaguer "nothing verified this".
//
// Both lookups go through an index rather than a direct Get, because one key and
// one DN can each name several certificates — renewals and link certificates for
// the same signing identity. They share a public key, so for verification any of
// them does; the reason to return all is that the store now holds all, and a
// lookup that silently picked one would put the old collapse back at a different
// layer.
//
// The revocation filter belongs here rather than in VerifyDsc because this is
// the one place every trust decision about an issuer passes through. Filtering
// at the call site would leave the next caller of issuerCandidates verifying
// against certificates governance has withdrawn.
func (k Keeper) issuerCandidates(ctx context.Context, dsc *certs.Cert) ([]*certs.Cert, bool, bool, error) {
	var out []*certs.Cert
	sawRevoked := false
	truncated := false
	seen := map[string]bool{}
	add := func(id []byte) error {
		if seen[string(id)] {
			return nil
		}
		// Stop collecting rather than fail. The budget bounds the work either
		// way, but refusing outright would mean that a country whose DN carries
		// more certificates than this could not register anybody at all, while
		// truncating still verifies every DSC whose issuer is among the ones
		// examined. A cap that locks out a country is a worse outcome than the
		// cost it was imposed to bound.
		//
		// Order is what makes that a good bet. The SKI lookup runs first and
		// matches on the authority key identifier, which names the signing key
		// itself, so a DSC carrying an AKI finds its real issuer in the first
		// entries and never reaches the ceiling. Only the DN sweep — the
		// fallback for a DSC with no AKI — can be cut short.
		//
		// The ceiling is on certificates examined, not on certificates
		// returned: parsing one and testing it for revocation is most of the
		// cost of considering it, so the check belongs before that work.
		if len(seen) >= types.MaxIssuerCandidates {
			truncated = true
			return nil
		}
		seen[string(id)] = true
		csca, err := k.Cscas.Get(ctx, id)
		if err != nil {
			return nil // an index entry with no record verifies nothing
		}
		pc, err := certs.ParseCert(csca.CertificateDer)
		if err != nil {
			return nil
		}
		// By key, not by certificate: siblings sharing this key are revoked with
		// it, which is the whole point of keying the set that way.
		revoked, err := k.IsCscaRevoked(ctx, pc.PublicKey.CanonicalBytes())
		if err != nil {
			return err
		}
		if revoked {
			sawRevoked = true
			return nil
		}
		out = append(out, pc)
		return nil
	}

	collect := func(idx collections.KeySet[collections.Pair[[]byte, []byte]], prefix []byte) error {
		rng := collections.NewPrefixedPairRange[[]byte, []byte](prefix)
		return idx.Walk(ctx, rng, func(key collections.Pair[[]byte, []byte]) (bool, error) {
			return false, add(key.K2())
		})
	}

	if len(dsc.AKI) > 0 {
		if err := collect(k.CscaBySKI, dsc.AKI); err != nil {
			return nil, false, false, err
		}
	}
	if err := collect(k.CscaByDN, dnKey(dsc.IssuerRaw)); err != nil {
		return nil, false, false, err
	}
	return out, sawRevoked, truncated, nil
}
