package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// Keeper is proof of personhood: the registered passports, the identity tree
// their holders prove membership of, and the private actions that membership
// buys (the daily ANML claim, the caretaker split). Nothing in it is keyed by
// an account: a registration is found by its passport nullifier, an identity
// by its tree leaf, and every action by a per-scope nullifier that only its
// holder can link to anything else.
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
	// burnRecorder counts what this module destroys, in x/earth.
	burnRecorder types.BurnRecorder
	// allocationKeeper owns the caretaker emission stream. This module files
	// its anonymous voters and draws its registration-reward pool down.
	allocationKeeper types.AllocationKeeper
	// shieldedKeeper mints this module's payouts as notes and verifies
	// membership proofs. Nil only in tests that never pay out.
	shieldedKeeper types.ShieldedKeeper

	// registration (proof-of-personhood)
	Registrations collections.Map[[]byte, types.Registration] // passport nullifier -> Registration
	RegCount      collections.Item[uint64]
	// Registration tallies by Document Signer and by issuing country.
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
	// SweepRetry: registrations a sweep failed to retire, passed over until
	// the time held (see sweepRetrying).
	SweepRetry collections.Map[[]byte, int64]

	// The identity tree and its anchors. See identity.go.
	IdentityNodes       collections.Map[collections.Pair[uint32, uint64], []byte]
	IdentitySize        collections.Item[uint64]
	IdentityRoots       collections.Map[[]byte, types.IdentityRoot]
	IdentityRootsByTime collections.KeySet[collections.Pair[int64, []byte]]
	LatestIdentityRoot  collections.Item[[]byte]

	// ClaimNullifiers is (day, nullifier) for the last two days' ANML claims.
	ClaimNullifiers collections.KeySet[collections.Pair[uint64, []byte]]

	// Caretaker split leases: nullifier -> expires_at, the expiry order, and
	// the live count.
	CaretakerVotes  collections.Map[[]byte, int64]
	CaretakerExpiry collections.KeySet[collections.Pair[int64, []byte]]
	CaretakerCount  collections.Item[uint64]

	// Handles: handle -> record, nullifier -> its active handle, and
	// (expires_at, handle) for the sweep. See handle.go.
	Handles       collections.Map[string, types.Handle]
	HandleByNf    collections.Map[[]byte, string]
	HandleRelease collections.KeySet[collections.Pair[int64, string]]
	// HandleMovedOut: handle nullifiers that moved their handle away (they
	// may never claim one again); HandleLeaseMax: the longest
	// handle_lease_seconds ever in force.
	HandleMovedOut collections.KeySet[[]byte]
	// CaretakerMovedOut: caretaker nullifiers that moved their split away.
	CaretakerMovedOut collections.KeySet[[]byte]
	HandleLeaseMax    collections.Item[int64]
	// PassportsSeen: passport nullifiers ever registered, each to the idc of
	// its last registration (a registration of one again is a re-entry or a
	// switch: its leaf's predecessor_at is set and a succession leaf from
	// that idc is appended). Successions: the identity tree's succession
	// leaves by index (circuits/move proves moves along them).
	PassportsSeen collections.Map[[]byte, []byte]
	Successions   collections.Map[uint64, types.Succession]
	// UsedIdcs: every identity commitment ever registered, by any passport.
	// A registration to one of them is refused (ErrIdcUsed): one idc is in
	// at most one passport's succession chain, and at most once in it, so a
	// switch back (A -> B -> A) cannot carry A's moved-out marks forward
	// (audit R2-B2), nor can two passports share an identity (R2-B1).
	UsedIdcs collections.KeySet[[]byte]

	// Registration bindings that have landed, refused for reuse until their
	// expiry, and the expiry order. See registration.go.
	UsedBindings      collections.Map[[]byte, int64]
	UsedBindingExpiry collections.KeySet[collections.Pair[int64, []byte]]

	// LeaseHold: see types.LeaseHold and leaseSeconds.
	LeaseHold collections.Item[types.LeaseHold]

	// buyback-and-burn clock
	LastBuyback     collections.Item[int64]
	TwapObservation collections.Item[math.LegacyDec]
	TwapObservedAt  collections.Item[int64]

	// pkiKeeper verifies the Document Signer behind each registration. Nil
	// only in tests.
	pkiKeeper types.PkiKeeper

	// checkTxProofs remembers passport proofs that verified in CheckTx (never
	// consulted in a block). See ultrahonk.VerifiedCache.
	checkTxProofs *ultrahonk.VerifiedCache
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
	shieldedKeeper types.ShieldedKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)
	pairBytes := collections.PairKeyCodec(collections.BytesKey, collections.BytesKey)
	timeBytes := collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)

	k := Keeper{
		storeService:     storeService,
		cdc:              cdc,
		addressCodec:     addressCodec,
		authority:        authority,
		bankKeeper:       bankKeeper,
		dexKeeper:        dexKeeper,
		burnRecorder:     burnRecorder,
		pkiKeeper:        pkiKeeper,
		allocationKeeper: allocationKeeper,
		shieldedKeeper:   shieldedKeeper,

		Params:            collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Registrations:     collections.NewMap(sb, types.RegistrationsKey, "registrations", collections.BytesKey, codec.CollValue[types.Registration](cdc)),
		RegCount:          collections.NewItem(sb, types.RegCountKey, "reg_count", collections.Uint64Value),
		RegCountByDsc:     collections.NewMap(sb, types.RegCountByDscKey, "reg_count_by_dsc", collections.BytesKey, collections.Uint64Value),
		RegCountByCountry: collections.NewMap(sb, types.RegCountByCountryKey, "reg_count_by_country", collections.StringKey, collections.Uint64Value),
		RegByRegisteredAt: collections.NewKeySet(sb, types.RegByRegisteredAtKey, "reg_by_registered_at", timeBytes),

		DscRate:     collections.NewMap(sb, types.DscRateKey, "dsc_rate", collections.BytesKey, codec.CollValue[types.RateCounter](cdc)),
		CountryRate: collections.NewMap(sb, types.CountryRateKey, "country_rate", collections.StringKey, codec.CollValue[types.RateCounter](cdc)),
		NetworkRate: collections.NewItem(sb, types.NetworkRateKey, "network_rate", codec.CollValue[types.RateCounter](cdc)),

		RegByDsc:        collections.NewKeySet(sb, types.RegByDscKey, "reg_by_dsc", pairBytes),
		PendingDscPurge: collections.NewKeySet(sb, types.PendingDscPurgeKey, "pending_dsc_purge", collections.BytesKey),
		SweepRetry:      collections.NewMap(sb, types.SweepRetryKey, "sweep_retry", collections.BytesKey, collections.Int64Value),

		IdentityNodes: collections.NewMap(sb, types.IdentityNodesKey, "identity_nodes",
			collections.PairKeyCodec(collections.Uint32Key, collections.Uint64Key), collections.BytesValue),
		IdentitySize: collections.NewItem(sb, types.IdentitySizeKey, "identity_size", collections.Uint64Value),
		IdentityRoots: collections.NewMap(sb, types.IdentityRootsKey, "identity_roots", collections.BytesKey,
			codec.CollValue[types.IdentityRoot](cdc)),
		IdentityRootsByTime: collections.NewKeySet(sb, types.IdentityRootsByTimeKey, "identity_roots_by_time", timeBytes),
		LatestIdentityRoot:  collections.NewItem(sb, types.LatestIdentityRootKey, "latest_identity_root", collections.BytesValue),

		ClaimNullifiers: collections.NewKeySet(sb, types.ClaimNullifiersKey, "claim_nullifiers",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey)),

		CaretakerVotes:  collections.NewMap(sb, types.CaretakerVotesKey, "caretaker_votes", collections.BytesKey, collections.Int64Value),
		CaretakerExpiry: collections.NewKeySet(sb, types.CaretakerExpiryKey, "caretaker_expiry", timeBytes),
		CaretakerCount:  collections.NewItem(sb, types.CaretakerCountKey, "caretaker_count", collections.Uint64Value),

		Handles: collections.NewMap(sb, types.HandlesKey, "handles", collections.StringKey,
			codec.CollValue[types.Handle](cdc)),
		HandleByNf: collections.NewMap(sb, types.HandleByNfKey, "handle_by_nf", collections.BytesKey, collections.StringValue),
		HandleRelease: collections.NewKeySet(sb, types.HandleReleaseKey, "handle_release",
			collections.PairKeyCodec(collections.Int64Key, collections.StringKey)),
		HandleMovedOut:    collections.NewKeySet(sb, types.HandleMovedOutKey, "handle_moved_out", collections.BytesKey),
		CaretakerMovedOut: collections.NewKeySet(sb, types.CaretakerMovedOutKey, "caretaker_moved_out", collections.BytesKey),
		HandleLeaseMax:    collections.NewItem(sb, types.HandleLeaseMaxKey, "handle_lease_max", collections.Int64Value),
		PassportsSeen:     collections.NewMap(sb, types.PassportsSeenKey, "passports_seen", collections.BytesKey, collections.BytesValue),
		Successions:       collections.NewMap(sb, types.SuccessionsKey, "successions", collections.Uint64Key, codec.CollValue[types.Succession](cdc)),
		UsedIdcs:          collections.NewKeySet(sb, types.UsedIdcsKey, "used_idcs", collections.BytesKey),

		UsedBindings:      collections.NewMap(sb, types.UsedBindingsKey, "used_bindings", collections.BytesKey, collections.Int64Value),
		UsedBindingExpiry: collections.NewKeySet(sb, types.UsedBindingExpiryKey, "used_binding_expiry", timeBytes),

		LeaseHold: collections.NewItem(sb, types.LeaseHoldKey, "lease_hold", codec.CollValue[types.LeaseHold](cdc)),

		checkTxProofs: ultrahonk.NewVerifiedCache(256),

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
