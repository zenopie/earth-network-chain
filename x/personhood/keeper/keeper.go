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

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// authority is the address that can execute MsgUpdateParams.
	authority []byte

	Schema collections.Schema
	Params collections.Item[types.Params]

	bankKeeper types.BankKeeper
	dexKeeper  types.DexKeeper
	// allocationKeeper owns the human emission stream. This module supplies that
	// stream's weight source and draws its registration-reward pool down; it
	// stores none of the stream's state itself.
	allocationKeeper types.AllocationKeeper

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

	// Daily registration counters behind the per-signer and per-country rate
	// caps — the automatic brake on a compromised Document Signer.
	DscRate     collections.Map[[]byte, types.RateCounter]
	CountryRate collections.Map[string, types.RateCounter]
	NetworkRate collections.Item[types.RateCounter]

	// RegByDsc indexes registrations by their Document Signer so a revoked
	// signer's registrations can be retired without scanning every registration
	// on the chain. PendingDscPurge is the set still being worked through.
	RegByDsc        collections.KeySet[collections.Pair[[]byte, []byte]]
	PendingDscPurge collections.KeySet[[]byte]

	// buyback-and-burn clock
	LastBuyback collections.Item[int64]
	// The buyback's price observation: a reading of the dex price accumulator
	// and the block second it was taken. Held here rather than in the dex
	// because the oracle keeps no history — the window is defined by whoever is
	// averaging over it, and this is that window's near end.
	TwapObservation collections.Item[math.LegacyDec]
	TwapObservedAt  collections.Item[int64]

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
	allocationKeeper types.AllocationKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService:     storeService,
		cdc:              cdc,
		addressCodec:     addressCodec,
		authority:        authority,
		bankKeeper:       bankKeeper,
		dexKeeper:        dexKeeper,
		pkiKeeper:        pkiKeeper,
		allocationKeeper: allocationKeeper,

		Params:            collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Registrations:     collections.NewMap(sb, types.RegistrationsKey, "registrations", collections.BytesKey, codec.CollValue[types.Registration](cdc)),
		RegByAddr:         collections.NewMap(sb, types.RegByAddrKey, "reg_by_addr", collections.BytesKey, collections.BytesValue),
		RegCount:          collections.NewItem(sb, types.RegCountKey, "reg_count", collections.Uint64Value),
		RegCountByDsc:     collections.NewMap(sb, types.RegCountByDscKey, "reg_count_by_dsc", collections.BytesKey, collections.Uint64Value),
		RegCountByCountry: collections.NewMap(sb, types.RegCountByCountryKey, "reg_count_by_country", collections.StringKey, collections.Uint64Value),
		RegByRegisteredAt: collections.NewKeySet(sb, types.RegByRegisteredAtKey, "reg_by_registered_at",
			collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),

		DscRate:     collections.NewMap(sb, types.DscRateKey, "dsc_rate", collections.BytesKey, codec.CollValue[types.RateCounter](cdc)),
		CountryRate: collections.NewMap(sb, types.CountryRateKey, "country_rate", collections.StringKey, codec.CollValue[types.RateCounter](cdc)),
		NetworkRate: collections.NewItem(sb, types.NetworkRateKey, "network_rate", codec.CollValue[types.RateCounter](cdc)),

		RegByDsc: collections.NewKeySet(sb, types.RegByDscKey, "reg_by_dsc",
			collections.PairKeyCodec(collections.BytesKey, collections.BytesKey)),
		PendingDscPurge: collections.NewKeySet(sb, types.PendingDscPurgeKey, "pending_dsc_purge", collections.BytesKey),

		LastBuyback:     collections.NewItem(sb, types.LastBuybackKey, "last_buyback", collections.Int64Value),
		TwapObservation: collections.NewItem(sb, types.TwapObservationKey, "twap_observation", sdk.LegacyDecValue),
		TwapObservedAt:  collections.NewItem(sb, types.TwapObservedAtKey, "twap_observed_at", collections.Int64Value),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}

// Weight implements the human stream's allocation weight source: every live
// registration carries the same fixed weight, and anything else carries none.
// That equality is the whole of one-human-one-vote — there is no scaling knob
// here on purpose.
func (k Keeper) Weight(ctx context.Context, addr []byte) (math.Int, error) {
	reg, ok, err := k.getRegistrationByAddr(ctx, addr)
	if err != nil {
		return math.Int{}, err
	}
	if !ok {
		return math.ZeroInt(), nil
	}
	expired, err := k.isExpired(ctx, reg)
	if err != nil {
		return math.Int{}, err
	}
	if expired {
		return math.ZeroInt(), nil
	}
	return math.NewInt(types.VoterWeight), nil
}
