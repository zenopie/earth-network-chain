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

	"github.com/earth-network/earth/x/deflation/types"
)

// IntegratedHandler resolves an INTEGRATED allocation option's accrued ERTH each
// block (e.g. compounding into dex pools); returns the amount resolved.
type IntegratedHandler func(ctx context.Context, accrued math.Int) (resolved math.Int, err error)

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// Address capable of executing a MsgUpdateParams message.
	// Typically, this should be the x/gov module account.
	authority []byte

	Schema collections.Schema
	Params collections.Item[types.Params]

	bankKeeper    types.BankKeeper
	stakingKeeper types.StakingKeeper
	dexKeeper     types.DexKeeper
	earthKeeper   types.EarthKeeper

	// --- stake-weighted allocation stream ---
	AllocationOptions collections.Map[uint64, types.AllocationOption]
	AllocationSeq     collections.Sequence
	Voters            collections.Map[[]byte, types.Voter]
	RewardIndex       collections.Item[math.Int]
	TotalWeight       collections.Item[math.Int]
	LastAllocUpkeep   collections.Item[int64]
	// AllocationEpoch is bumped by a governance reset to retire every vote at once.
	AllocationEpoch collections.Item[uint64]
	IntegratedOptions  collections.KeySet[uint64]
	integratedHandlers map[string]IntegratedHandler
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
	stakingKeeper types.StakingKeeper,
	dexKeeper types.DexKeeper,
	earthKeeper types.EarthKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService:  storeService,
		cdc:           cdc,
		addressCodec:  addressCodec,
		authority:     authority,
		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,
		dexKeeper:     dexKeeper,
		earthKeeper:   earthKeeper,

		Params: collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),

		AllocationOptions: collections.NewMap(sb, types.AllocationOptionsKey, "allocation_options", collections.Uint64Key, codec.CollValue[types.AllocationOption](cdc)),
		AllocationSeq:     collections.NewSequence(sb, types.AllocationSeqKey, "allocation_seq"),
		Voters:            collections.NewMap(sb, types.VotersKey, "voters", collections.BytesKey, codec.CollValue[types.Voter](cdc)),
		RewardIndex:       collections.NewItem(sb, types.RewardIndexKey, "reward_index", sdk.IntValue),
		TotalWeight:       collections.NewItem(sb, types.TotalWeightKey, "total_weight", sdk.IntValue),
		LastAllocUpkeep:   collections.NewItem(sb, types.LastAllocUpkeepKey, "last_alloc_upkeep", collections.Int64Value),
		AllocationEpoch:   collections.NewItem(sb, types.AllocationEpochKey, "allocation_epoch", collections.Uint64Value),
		IntegratedOptions: collections.NewKeySet(sb, types.IntegratedOptionsKey, "integrated_options", collections.Uint64Key),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	// Register the compiled-in integrated-allocation handlers (gov may only add
	// INTEGRATED options whose handler is in this set).
	k.integratedHandlers = map[string]IntegratedHandler{
		types.HandlerLPRewards: func(ctx context.Context, accrued math.Int) (math.Int, error) {
			return dexKeeper.DistributeLPRewards(ctx, accrued)
		},
	}

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}

// HubDenom returns the hub asset denom (the staking coin, ERTH).
func (k Keeper) HubDenom(ctx context.Context) (string, error) {
	return k.stakingKeeper.BondDenom(ctx)
}
