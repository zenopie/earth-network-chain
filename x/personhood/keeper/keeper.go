package keeper

import (
	"context"
	"errors"
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
	// burnRecorder counts what this module destroys, in x/earth. Burning is
	// invisible after the block that does it, so it is recorded as it happens.
	burnRecorder types.BurnRecorder
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

	// retirementListeners are told when a registration stops counting. A
	// pointer, so the copies of Keeper that module wiring hands around share
	// one list and a listener registered on any of them is heard by all.
	retirementListeners *[]types.RetirementListener
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
	burnRecorder types.BurnRecorder,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		retirementListeners: &[]types.RetirementListener{},
		storeService:        storeService,
		cdc:                 cdc,
		addressCodec:        addressCodec,
		authority:           authority,
		bankKeeper:          bankKeeper,
		dexKeeper:           dexKeeper,
		burnRecorder:        burnRecorder,
		pkiKeeper:           pkiKeeper,
		allocationKeeper:    allocationKeeper,

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

// LiveNullifier returns the nullifier of addr's live registration, for x/assembly.
//
// The nullifier rather than the address is what the chamber records a vote
// against, and that is the whole point of this method existing. A registration
// can be moved to a new wallet — MsgRegister rebinds it, carrying the ANML clock
// with it — so a vote filed under the address it was cast from could be cast
// again from the next one. The nullifier is derived from the passport and does
// not move, so one person is one vote however many wallets they hold.
//
// A lapsed registration reports false, the same as no registration at all: the
// franchise is live registrations, checked when the vote is cast.
func (k Keeper) LiveNullifier(ctx context.Context, addr []byte) ([]byte, bool, error) {
	reg, ok, err := k.getRegistrationByAddr(ctx, addr)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	live, err := k.isLive(ctx, reg)
	if err != nil || !live {
		return nil, false, err
	}
	return reg.Nullifier, true, nil
}

// RegistrationDsc returns the Document Signer commitment of the registration
// filed under nullifier, for x/assembly to keep a signer's registrations out of
// the vote on revoking it. Nil when there is no such registration.
func (k Keeper) RegistrationDsc(ctx context.Context, nullifier []byte) ([]byte, error) {
	reg, err := k.Registrations.Get(ctx, nullifier)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return reg.DscKey, nil
}

// RegisterRetirementListener attaches a listener. Called once, from module
// wiring, by whichever module files records under a nullifier.
func (k Keeper) RegisterRetirementListener(l types.RetirementListener) {
	*k.retirementListeners = append(*k.retirementListeners, l)
}

// retireRegistration removes a registration that has stopped counting as a
// human and tells the listeners. Every removal goes through here except a
// wallet switch, which calls removeRegistration directly — see
// RetirementListener.
func (k Keeper) retireRegistration(ctx context.Context, reg types.Registration) error {
	if err := k.removeRegistration(ctx, reg); err != nil {
		return err
	}
	for _, l := range *k.retirementListeners {
		if err := l.OnRegistrationRetired(ctx, reg.Nullifier); err != nil {
			return err
		}
	}
	return nil
}

// isLive is the franchise test the chamber and the allocation stream share:
// unexpired, and not signed by a revoked Document Signer.
//
// Revocation has to be checked here and not left to the purge sweep. The sweep
// is bounded per block, so a revoked signer's registrations stay in state for
// as long as it takes to reach them, and every one of them would otherwise keep
// voting and keep its allocation weight until then — including on the proposal
// that revoked their signer.
func (k Keeper) isLive(ctx context.Context, reg types.Registration) (bool, error) {
	if err := k.checkNotExpiredOrRevoked(ctx, reg); err != nil {
		if errors.Is(err, types.ErrRegExpired) || errors.Is(err, types.ErrDscRevoked) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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
	live, err := k.isLive(ctx, reg)
	if err != nil {
		return math.Int{}, err
	}
	if !live {
		return math.ZeroInt(), nil
	}
	return math.NewInt(types.VoterWeight), nil
}
