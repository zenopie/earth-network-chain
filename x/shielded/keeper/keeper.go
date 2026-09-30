package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/shielded/types"
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

	poolAddr sdk.AccAddress
	// shieldedOnly are the denoms that may only move to shieldedOnlyTo.
	shieldedOnly   map[string]bool
	shieldedOnlyTo map[string]bool // module account address (string bytes)

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

	PrivateTxCount collections.Item[uint64]

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
		storeService:   storeService,
		cdc:            cdc,
		addressCodec:   addressCodec,
		authority:      authority,
		authKeeper:     authKeeper,
		bankKeeper:     bankKeeper,
		poolAddr:       authtypes.NewModuleAddress(types.ModuleName),
		shieldedOnly:   map[string]bool{},
		shieldedOnlyTo: map[string]bool{},
		actions:        map[string]types.PrivateActionHandler{},

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

		PrivateTxCount: collections.NewItem(sb, types.PrivateTxCountKey, "private_tx_count", collections.Uint64Value),
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

// IsShieldedOnly reports whether denom exists only in the pool.
func (k Keeper) IsShieldedOnly(denom string) bool { return k.shieldedOnly[denom] }

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
