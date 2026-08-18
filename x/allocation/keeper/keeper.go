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

	"github.com/earth-network/earth/x/allocation/types"
)

// IntegratedHandler resolves an INTEGRATED option's accrued ERTH each block
// (e.g. compounding into dex pools); returns the amount resolved. Whatever it
// leaves behind stays accrued on the option and is offered again next block.
type IntegratedHandler func(ctx context.Context, accrued math.Int) (resolved math.Int, err error)

// integratedHandler is a handler bound to the one stream it belongs to, so a
// governance proposal cannot attach the human stream's registration-reward pool
// to the capital stream or vice versa.
type integratedHandler struct {
	stream types.StreamId
	fn     IntegratedHandler
}

// Keeper owns both emission streams. All state is keyed by stream id first: the
// two streams run the same engine over disjoint state, differing only in the
// WeightSource that decides who may vote and with how much weight.
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

	// --- per-stream allocation state ---
	Options           collections.Map[collections.Pair[uint32, uint64], types.AllocationOption]
	OptionSeq         collections.Map[uint32, uint64]
	Voters            collections.Map[collections.Pair[uint32, []byte], types.Voter]
	RewardIndex       collections.Map[uint32, math.Int]
	TotalWeight       collections.Map[uint32, math.Int]
	LastUpkeep        collections.Map[uint32, int64]
	Epoch             collections.Map[uint32, uint64]
	IntegratedOptions collections.KeySet[collections.Pair[uint32, uint64]]

	// weightSources and integratedHandlers are maps rather than fields because
	// they are populated after construction, by the modules that own the
	// behaviour: x/personhood supplies the human weight source, x/dex the
	// lp_rewards handler. The Keeper is copied by value all over the app, so the
	// registries have to be reference types for a late registration to be seen
	// by every copy.
	weightSources      map[types.StreamId]types.WeightSource
	integratedHandlers map[string]integratedHandler
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
	stakingKeeper types.StakingKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)
	streamOption := collections.PairKeyCodec(collections.Uint32Key, collections.Uint64Key)

	k := Keeper{
		storeService:  storeService,
		cdc:           cdc,
		addressCodec:  addressCodec,
		authority:     authority,
		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,

		Params: collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),

		Options:   collections.NewMap(sb, types.OptionsKey, "options", streamOption, codec.CollValue[types.AllocationOption](cdc)),
		OptionSeq: collections.NewMap(sb, types.OptionSeqKey, "option_seq", collections.Uint32Key, collections.Uint64Value),
		Voters: collections.NewMap(sb, types.VotersKey, "voters",
			collections.PairKeyCodec(collections.Uint32Key, collections.BytesKey), codec.CollValue[types.Voter](cdc)),
		RewardIndex:       collections.NewMap(sb, types.RewardIndexKey, "reward_index", collections.Uint32Key, sdk.IntValue),
		TotalWeight:       collections.NewMap(sb, types.TotalWeightKey, "total_weight", collections.Uint32Key, sdk.IntValue),
		LastUpkeep:        collections.NewMap(sb, types.LastUpkeepKey, "last_upkeep", collections.Uint32Key, collections.Int64Value),
		Epoch:             collections.NewMap(sb, types.EpochKey, "epoch", collections.Uint32Key, collections.Uint64Value),
		IntegratedOptions: collections.NewKeySet(sb, types.IntegratedOptionsKey, "integrated_options", streamOption),

		weightSources:      map[types.StreamId]types.WeightSource{},
		integratedHandlers: map[string]integratedHandler{},
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	// The capital stream's weight source lives here rather than in x/earth: this
	// module already holds both halves of it (bonded stake from staking, the
	// compounding index from earth) and owns the staking hooks that keep it
	// current.
	k.weightSources[types.STREAM_ID_CAPITAL] = capitalWeightSource{k: k}

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

// RegisterWeightSource attaches a stream's weight source. Called once, from
// module wiring, by whichever module owns the notion of weight for that stream.
func (k Keeper) RegisterWeightSource(stream types.StreamId, src types.WeightSource) {
	k.weightSources[stream] = src
}

// RegisterIntegratedHandler registers an INTEGRATED handler for one stream.
// Governance may only add an INTEGRATED option naming a handler registered here,
// and only in the stream it was registered for.
func (k Keeper) RegisterIntegratedHandler(stream types.StreamId, name string, fn IntegratedHandler) {
	k.integratedHandlers[name] = integratedHandler{stream: stream, fn: fn}
}

// weightSource returns the stream's weight source, or an error naming the stream
// if none was wired up — which is a wiring bug, not a user error, but is worth
// surfacing as a rejected message rather than a nil dereference.
func (k Keeper) weightSource(stream types.StreamId) (types.WeightSource, error) {
	src, ok := k.weightSources[stream]
	if !ok {
		return nil, types.ErrUnknownStream.Wrapf("no weight source registered for %s", stream)
	}
	return src, nil
}

// capitalWeightSource resolves a staker's weight: their bonded stake. No bond
// means no vote.
type capitalWeightSource struct{ k Keeper }

func (s capitalWeightSource) Weight(ctx context.Context, addr []byte) (math.Int, error) {
	return s.k.stakingKeeper.GetDelegatorBonded(ctx, sdk.AccAddress(addr))
}
