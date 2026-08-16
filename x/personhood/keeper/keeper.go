package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// IntegratedHandler resolves an INTEGRATED democratic option accrued ERTH each
// block; returns the amount resolved.
type IntegratedHandler func(ctx context.Context, accrued math.Int) (resolved math.Int, err error)

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// authority is the address that can execute MsgUpdateParams / MsgAddIntegratedOption.
	authority []byte

	Schema collections.Schema
	Params collections.Item[types.Params]

	bankKeeper types.BankKeeper
	dexKeeper  types.DexKeeper

	// registration (proof-of-personhood)
	Registrations collections.Map[[]byte, types.Registration] // nullifier -> Registration
	RegByAddr     collections.Map[[]byte, []byte]             // addr -> nullifier
	RegCount      collections.Item[uint64]
	// Registration tallies by Document Signer and by issuing country. Kept as
	// counters rather than derived by walking every registration, so the
	// explorer's per-country map is a cheap read at any scale.
	RegCountByDsc     collections.Map[[]byte, uint64]
	RegCountByCountry collections.Map[string, uint64]
	// RegByRegisteredAt orders registrations by registration time so BeginBlocker
	// can retire the lapsed ones without walking the whole set.
	RegByRegisteredAt collections.KeySet[collections.Pair[int64, []byte]]

	// democratic allocation stream (one-human-one-vote)
	DemOptions     collections.Map[uint64, types.DemocraticOption]
	DemSeq         collections.Sequence
	DemVoters      collections.Map[[]byte, types.DemocraticVoter]
	DemRewardIndex collections.Item[math.Int]
	DemTotalWeight collections.Item[math.Int]
	DemLastUpkeep  collections.Item[int64]
	// DemEpoch is bumped by a governance reset to retire every vote at once.
	DemEpoch collections.Item[uint64]

	// buyback-and-burn clock
	LastBuyback          collections.Item[int64]
	DemIntegratedOptions collections.KeySet[uint64]
	integratedHandlers   map[string]IntegratedHandler

	// pkiKeeper binds registration proofs to the live DSC-registry root history
	// (Option C). Optional: nil falls back to the static params.DscRoot check.
	pkiKeeper types.PkiKeeper
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
	dexKeeper types.DexKeeper,
	pkiKeeper types.PkiKeeper,
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
		bankKeeper:   bankKeeper,
		dexKeeper:    dexKeeper,
		pkiKeeper:    pkiKeeper,

		Params:            collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Registrations:     collections.NewMap(sb, types.RegistrationsKey, "registrations", collections.BytesKey, codec.CollValue[types.Registration](cdc)),
		RegByAddr:         collections.NewMap(sb, types.RegByAddrKey, "reg_by_addr", collections.BytesKey, collections.BytesValue),
		RegCount:          collections.NewItem(sb, types.RegCountKey, "reg_count", collections.Uint64Value),
		RegCountByDsc:     collections.NewMap(sb, types.RegCountByDscKey, "reg_count_by_dsc", collections.BytesKey, collections.Uint64Value),
		RegCountByCountry: collections.NewMap(sb, types.RegCountByCountryKey, "reg_count_by_country", collections.StringKey, collections.Uint64Value),
		RegByRegisteredAt: collections.NewKeySet(sb, types.RegByRegisteredAtKey, "reg_by_registered_at",
			collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),

		DemOptions:           collections.NewMap(sb, types.DemOptionsKey, "dem_options", collections.Uint64Key, codec.CollValue[types.DemocraticOption](cdc)),
		DemSeq:               collections.NewSequence(sb, types.DemSeqKey, "dem_seq"),
		DemVoters:            collections.NewMap(sb, types.DemVotersKey, "dem_voters", collections.BytesKey, codec.CollValue[types.DemocraticVoter](cdc)),
		DemRewardIndex:       collections.NewItem(sb, types.DemRewardIndexKey, "dem_reward_index", sdk.IntValue),
		DemTotalWeight:       collections.NewItem(sb, types.DemTotalWeightKey, "dem_total_weight", sdk.IntValue),
		DemLastUpkeep:        collections.NewItem(sb, types.DemLastUpkeepKey, "dem_last_upkeep", collections.Int64Value),
		DemEpoch:             collections.NewItem(sb, types.DemEpochKey, "dem_epoch", collections.Uint64Value),
		LastBuyback:          collections.NewItem(sb, types.LastBuybackKey, "last_buyback", collections.Int64Value),
		DemIntegratedOptions: collections.NewKeySet(sb, types.DemIntegratedOptionsKey, "dem_integrated_options", collections.Uint64Key),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	// Compiled-in integrated handlers (gov may only add INTEGRATED options whose
	// handler is registered here).
	k.integratedHandlers = map[string]IntegratedHandler{
		types.HandlerRegistrationRewards: func(context.Context, math.Int) (math.Int, error) {
			// Pool accrues each block; paid out on registration (payRegistrationReward).
			return math.ZeroInt(), nil
		},
	}

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}
