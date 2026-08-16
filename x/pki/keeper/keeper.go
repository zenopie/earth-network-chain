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
	Cscas collections.Map[[]byte, types.Csca] // cscaID -> Csca
	// Indexed by a hash of the subject DN rather than the DN itself: collections
	// length-prefixes a non-terminal bytes key with a single byte, so a DN longer
	// than 255 bytes cannot be encoded — and real ICAO subject DNs do exceed that.
	// Hashing makes the key fixed-size while preserving the exact-equality
	// matching that issuer lookup needs.
	CscaByDN collections.KeySet[collections.Pair[[]byte, []byte]] // (sha256(subjectDN), cscaID)

	// Revoked Document Signers, keyed by sha256(canonical pubkey) — the same
	// identity VerifyDsc checks, so revocation covers every certificate carrying
	// that key.
	RevokedDscs collections.KeySet[[]byte]
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

		Params:      collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Cscas:       collections.NewMap(sb, types.CscasKey, "cscas", collections.BytesKey, codec.CollValue[types.Csca](cdc)),
		CscaByDN:    collections.NewKeySet(sb, types.CscaByDNKey, "csca_by_dn", collections.PairKeyCodec(collections.BytesKey, collections.BytesKey)),
		RevokedDscs: collections.NewKeySet(sb, types.RevokedDscsKey, "revoked_dscs", collections.BytesKey),
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
