package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/earth-network/earth/x/pki/types"
)

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// authority can execute MsgUpdateParams / MsgAddCsca (defaults to x/gov).
	authority []byte

	Schema collections.Schema
	Params collections.Item[types.Params]

	// CSCA master list (governance-managed).
	//
	// Keyed by certID — sha256 of the certificate's own DER — so the store holds
	// one entry per certificate. It used to be keyed by SKI, which is one entry
	// per *signing key*, and the master list carries several certificates per key:
	// 536 certificates under 366 SKIs, the extras being renewals and link
	// certificates. Keying by SKI meant they overwrote each other and 170
	// certificate bodies were dropped at InitGenesis, never reaching the chain.
	Cscas collections.Map[[]byte, types.Csca] // certID -> Csca

	// CscaBySKI is the primary issuer lookup. A DSC's AuthorityKeyIdentifier is
	// by convention its issuer's SubjectKeyIdentifier, so this is the index that
	// answers "which certificates carry the key that signed this". Several
	// certificates may share one SKI; they share a public key too, so any of them
	// verifies the signature, and the group is 2-3 entries in practice.
	CscaBySKI collections.KeySet[collections.Pair[[]byte, []byte]] // (SKI, certID)

	// Indexed by a hash of the subject DN rather than the DN itself: collections
	// length-prefixes a non-terminal bytes key with a single byte, so a DN longer
	// than 255 bytes cannot be encoded — and real ICAO subject DNs do exceed that.
	// Hashing makes the key fixed-size while preserving the exact-equality
	// matching that issuer lookup needs.
	CscaByDN collections.KeySet[collections.Pair[[]byte, []byte]] // (sha256(subjectDN), certID)

	// Revoked Document Signers, keyed by sha256(canonical pubkey) — the same
	// identity VerifyDsc checks, so revocation covers every certificate carrying
	// that key.
	RevokedDscs collections.KeySet[[]byte]
	// The same revocations keyed by Poseidon2 commitment — the identity a
	// Registration stores and the register circuit exposes. Written alongside
	// RevokedDscs so a revoked signer can be recognised from either direction:
	// by certificate at registration time, and by commitment for registrations
	// already on the books.
	RevokedDscCommitments collections.KeySet[[]byte]

	// Revoked CSCA signing keys, keyed by sha256(canonical pubkey).
	//
	// Keyed by the signing key and not by certificate for the reason RevokedDscs
	// is: the store holds several certificates per key — renewals and link
	// certificates — and any of them verifies a child signature, so a revocation
	// naming one certificate would be undone by its siblings. Consulted in
	// issuerCandidates, which is the single point every trust decision about an
	// issuer passes through.
	RevokedCscas collections.KeySet[[]byte]

	// revocationListeners are notified when a Document Signer is revoked, so the
	// modules holding records made under it can act without this module needing
	// to know they exist.
	//
	// A registry rather than a direct call because the dependency only runs one
	// way: x/personhood imports this module to verify certificates, so this
	// module cannot import it back to retire its registrations. The listener is
	// attached at wiring time by whoever owns those records — the same pattern
	// x/allocation uses for its weight sources, and for the same reason.
	revocationListeners *[]types.DscRevocationListener
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}
	sb := collections.NewSchemaBuilder(storeService)
	k := Keeper{
		storeService: storeService,
		cdc:          cdc,
		addressCodec: addressCodec,
		authority:    authority,

		Params:                collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Cscas:                 collections.NewMap(sb, types.CscasKey, "cscas", collections.BytesKey, codec.CollValue[types.Csca](cdc)),
		CscaBySKI:             collections.NewKeySet(sb, types.CscaBySKIKey, "csca_by_ski", collections.PairKeyCodec(collections.BytesKey, collections.BytesKey)),
		CscaByDN:              collections.NewKeySet(sb, types.CscaByDNKey, "csca_by_dn", collections.PairKeyCodec(collections.BytesKey, collections.BytesKey)),
		revocationListeners:   &[]types.DscRevocationListener{},
		RevokedDscs:           collections.NewKeySet(sb, types.RevokedDscsKey, "revoked_dscs", collections.BytesKey),
		RevokedDscCommitments: collections.NewKeySet(sb, types.RevokedDscCommitmentsKey, "revoked_dsc_commitments", collections.BytesKey),
		RevokedCscas:          collections.NewKeySet(sb, types.RevokedCscasKey, "revoked_cscas", collections.BytesKey),
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// GetAuthority returns the module authority.
func (k Keeper) GetAuthority() []byte { return k.authority }
