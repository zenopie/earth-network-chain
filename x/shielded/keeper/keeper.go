package keeper

import (
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// Keeper is the shielded pool: the note tree, the spent set, the root
// history, the asset registry and per-asset turnstiles, plus the pool's coins
// in this module's account.
type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// authority is the address that can execute MsgUpdateParams and
	// MsgRegisterAsset.
	authority []byte

	authKeeper types.AuthKeeper
	bankKeeper types.BankKeeper

	// proofVerifier is zk/ultrahonk.Verify (tests count calls through it).
	proofVerifier func(vk, proof []byte, publicInputs [][]byte) (bool, error)
	// checkTxProofs remembers proofs that verified in CheckTx (never
	// consulted in a block). See ultrahonk.VerifiedCache.
	checkTxProofs *ultrahonk.VerifiedCache

	poolAddr sdk.AccAddress
	// shieldedOnly are the denoms that may only move to shieldedOnlyTo.
	shieldedOnly   map[string]bool
	shieldedOnlyTo map[string]bool // module account address (string bytes)
	// shieldedOnlyPrefixes are families of shielded-only denoms registered by
	// other modules (x/shieldedstaking's derth/), each with its own
	// allowed recipients (module account address, string bytes). A map so every
	// copy of the keeper sees a registration made during module wiring.
	shieldedOnlyPrefixes map[string]map[string]bool
	// poolLockedPrefixes are families of denoms whose notes leave the pool
	// only to a module, through a private msg of it (ReleaseToModule), never
	// by an unshield: x/dex's LP shares, withdrawn by
	// MsgRemoveLiquidityShielded.
	poolLockedPrefixes map[string]bool
	// excludedAssetPrefixes are families of denoms the pool never admits as
	// assets: x/shieldedstaking's derth/, which lives in its own
	// owner-locked stake note tree.
	excludedAssetPrefixes map[string]bool

	Schema collections.Schema
	Params collections.Item[types.Params]

	TreeNodes   collections.Map[collections.Pair[uint32, uint64], []byte]
	TreeSize    collections.Item[uint64]
	Roots       collections.Map[[]byte, types.RootRecord]
	RootsByTime collections.KeySet[collections.Pair[int64, []byte]]
	LatestRoot  collections.Item[[]byte]
	Nullifiers  collections.KeySet[[]byte]

	Assets     collections.Map[string, []byte]
	AssetsByID collections.Map[[]byte, string]

	Turnstiles  collections.Map[string, types.Turnstile]
	DirtyDenoms collections.KeySet[string]

	PrivateActionCount collections.Item[uint64]

	// actions are the private actions other modules attach to their private
	// msgs, by msg type URL. A map, so the copies of Keeper that module wiring
	// hands around share one registry. See types.PrivateActionHandler.
	actions map[string]types.PrivateActionHandler
}

// NewKeeper builds the keeper. shieldedOnlyDenoms may be held only by the
// modules named in shieldedOnlyRecipients (and this one); see SendRestriction.
func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,
	authKeeper types.AuthKeeper,
	bankKeeper types.BankKeeper,
	shieldedOnlyDenoms []string,
	shieldedOnlyRecipients []string,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}
	sb := collections.NewSchemaBuilder(storeService)
	k := Keeper{
		storeService:          storeService,
		cdc:                   cdc,
		addressCodec:          addressCodec,
		authority:             authority,
		authKeeper:            authKeeper,
		bankKeeper:            bankKeeper,
		proofVerifier:         ultrahonk.Verify,
		checkTxProofs:         ultrahonk.NewVerifiedCache(types.CheckTxProofCacheSize),
		poolAddr:              authtypes.NewModuleAddress(types.ModuleName),
		shieldedOnly:          map[string]bool{},
		shieldedOnlyTo:        map[string]bool{},
		shieldedOnlyPrefixes:  map[string]map[string]bool{},
		poolLockedPrefixes:    map[string]bool{},
		excludedAssetPrefixes: map[string]bool{},
		actions:               map[string]types.PrivateActionHandler{},

		Params: collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),

		TreeNodes: collections.NewMap(sb, types.TreeNodesKey, "tree_nodes",
			collections.PairKeyCodec(collections.Uint32Key, collections.Uint64Key), collections.BytesValue),
		TreeSize: collections.NewItem(sb, types.TreeSizeKey, "tree_size", collections.Uint64Value),
		Roots: collections.NewMap(sb, types.RootsKey, "roots", collections.BytesKey,
			codec.CollValue[types.RootRecord](cdc)),
		RootsByTime: collections.NewKeySet(sb, types.RootsByTimeKey, "roots_by_time",
			collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),
		LatestRoot: collections.NewItem(sb, types.LatestRootKey, "latest_root", collections.BytesValue),
		Nullifiers: collections.NewKeySet(sb, types.NullifiersKey, "nullifiers", collections.BytesKey),

		Assets:     collections.NewMap(sb, types.AssetsKey, "assets", collections.StringKey, collections.BytesValue),
		AssetsByID: collections.NewMap(sb, types.AssetsByIDKey, "assets_by_id", collections.BytesKey, collections.StringValue),

		Turnstiles: collections.NewMap(sb, types.TurnstilesKey, "turnstiles", collections.StringKey,
			codec.CollValue[types.Turnstile](cdc)),
		DirtyDenoms: collections.NewKeySet(sb, types.DirtyDenomsKey, "dirty_denoms", collections.StringKey),

		PrivateActionCount: collections.NewItem(sb, types.PrivateActionCountKey, "private_action_count", collections.Uint64Value),
	}
	for _, d := range shieldedOnlyDenoms {
		k.shieldedOnly[d] = true
	}
	k.shieldedOnlyTo[string(k.poolAddr)] = true
	for _, m := range shieldedOnlyRecipients {
		k.shieldedOnlyTo[string(authtypes.NewModuleAddress(m))] = true
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte { return k.authority }

// PoolAddress is the module account holding every shielded coin.
func (k Keeper) PoolAddress() sdk.AccAddress { return k.poolAddr }

// AddressCodec is the account address codec (signals bind raw address bytes).
func (k Keeper) AddressCodec() address.Codec { return k.addressCodec }

// ShieldedOnlyDenoms lists the denoms that exist only in the pool.
func (k Keeper) ShieldedOnlyDenoms() []string {
	out := make([]string, 0, len(k.shieldedOnly))
	for d := range k.shieldedOnly {
		out = append(out, d)
	}
	return out
}

// RegisterPoolLockedPrefix makes the notes of every denom starting with prefix
// leave the pool only through a module's private msg (ReleaseToModule): an
// unshield of one (MsgSend to a receiver) is refused before anything is
// spent. Unlike a shielded-only denom it may still exist outside the pool
// (x/dex's transparent LP shares). Called once per prefix from module wiring.
func (k Keeper) RegisterPoolLockedPrefix(prefix string) {
	if prefix == "" {
		panic("empty pool-locked prefix")
	}
	k.poolLockedPrefixes[prefix] = true
}

// ExcludeAssetPrefix keeps every denom starting with prefix out of the pool's
// asset registry for good (RegisterAsset refuses it), so no note of it can
// ever exist in the pool. Called from module wiring.
func (k Keeper) ExcludeAssetPrefix(prefix string) {
	if prefix == "" {
		panic("empty excluded asset prefix")
	}
	k.excludedAssetPrefixes[prefix] = true
}

// IsExcludedAsset reports whether denom may never be a pool asset.
func (k Keeper) IsExcludedAsset(denom string) bool {
	for p := range k.excludedAssetPrefixes {
		if strings.HasPrefix(denom, p) {
			return true
		}
	}
	return false
}

// IsPoolLocked reports whether denom's notes may not be unshielded.
func (k Keeper) IsPoolLocked(denom string) bool {
	for p := range k.poolLockedPrefixes {
		if strings.HasPrefix(denom, p) {
			return true
		}
	}
	return false
}

// IsShieldedOnly reports whether denom exists only in the pool.
func (k Keeper) IsShieldedOnly(denom string) bool {
	if k.shieldedOnly[denom] {
		return true
	}
	_, ok := k.shieldedOnlyPrefix(denom)
	return ok
}

// RegisterShieldedOnlyPrefix makes every denom starting with prefix
// shielded-only: it may move only into the pool and into the named module
// accounts, never to an ordinary account. Called once per prefix from module
// wiring, by the module that mints those denoms, so the pool refuses an
// unshield of one before it spends anything.
func (k Keeper) RegisterShieldedOnlyPrefix(prefix string, recipientModules ...string) {
	if prefix == "" {
		panic("empty shielded-only prefix")
	}
	// Prefixes must not nest: a denom has to belong to one family, or which
	// recipient list applies would depend on map order.
	for p := range k.shieldedOnlyPrefixes {
		if strings.HasPrefix(p, prefix) || strings.HasPrefix(prefix, p) {
			panic(fmt.Sprintf("shielded-only prefix %q overlaps %q", prefix, p))
		}
	}
	to := map[string]bool{string(k.poolAddr): true}
	for _, m := range recipientModules {
		to[string(authtypes.NewModuleAddress(m))] = true
	}
	k.shieldedOnlyPrefixes[prefix] = to
}

// shieldedOnlyPrefix returns the allowed recipients of denom's registered
// prefix family, if it has one.
func (k Keeper) shieldedOnlyPrefix(denom string) (map[string]bool, bool) {
	for p, to := range k.shieldedOnlyPrefixes {
		if strings.HasPrefix(denom, p) {
			return to, true
		}
	}
	return nil, false
}

// RegisterPrivateAction attaches h to the private msg type msgTypeURL. Called
// once per type, from module wiring; a second registration panics.
func (k Keeper) RegisterPrivateAction(msgTypeURL string, h types.PrivateActionHandler) {
	if _, dup := k.actions[msgTypeURL]; dup {
		panic(fmt.Sprintf("private action for %s registered twice", msgTypeURL))
	}
	k.actions[msgTypeURL] = h
}

// PrivateAction returns the action handler registered for msg's type, if any.
func (k Keeper) PrivateAction(msg sdk.Msg) (types.PrivateActionHandler, bool) {
	h, ok := k.actions[sdk.MsgTypeURL(msg)]
	return h, ok
}
