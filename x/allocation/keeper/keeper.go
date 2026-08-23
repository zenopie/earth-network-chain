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
	SummedWeight      collections.Map[uint32, math.Int]
	LastUpkeep        collections.Map[uint32, int64]
	Epoch             collections.Map[uint32, uint64]
	IntegratedOptions collections.KeySet[collections.Pair[uint32, uint64]]
	// SummedAccrued is the running sum of every option's Accumulated. Compared
	// against the module's ERTH balance every block. See invariants.go.
	SummedAccrued collections.Item[math.Int]
	// Residue is minted emission no option can collect — index truncation.
	// Swept to the community pool. See allocation.go.
	Residue collections.Item[math.Int]
	// PruneQueue orders dead options by when they may be removed; PruneDue is
	// the reverse lookup that lets one cancel itself when it comes back to life.
	PruneQueue collections.KeySet[collections.Triple[int64, uint32, uint64]]
	PruneDue   collections.Map[collections.Pair[uint32, uint64], int64]

	// weightSources and integratedHandlers are maps rather than fields because
	// they are populated after construction, by the modules that own the
	// behaviour: x/personhood supplies the human weight source, x/dex the
	// lp_rewards handler. The Keeper is copied by value all over the app, so the
	// registries have to be reference types for a late registration to be seen
	// by every copy.
	weightSources      map[types.StreamId]types.WeightSource
	integratedHandlers map[string]integratedHandler
	// residueSink is where truncation dust goes, registered from app.go for the
	// same reason the community-pool handler is. A pointer for the same reason
	// the maps above are: the Keeper is copied by value, so a late registration
	// has to be visible through a reference.
	residueSink *residueSink
}

// residueSink holds the late-registered dust sink. See SweepResidue.
type residueSink struct {
	fn func(ctx context.Context, amount math.Int) error
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
		SummedWeight:      collections.NewMap(sb, types.SummedWeightKey, "summed_weight", collections.Uint32Key, sdk.IntValue),
		LastUpkeep:        collections.NewMap(sb, types.LastUpkeepKey, "last_upkeep", collections.Uint32Key, collections.Int64Value),
		Epoch:             collections.NewMap(sb, types.EpochKey, "epoch", collections.Uint32Key, collections.Uint64Value),
		IntegratedOptions: collections.NewKeySet(sb, types.IntegratedOptionsKey, "integrated_options", streamOption),
		SummedAccrued:     collections.NewItem(sb, types.SummedAccruedKey, "summed_accrued", sdk.IntValue),
		Residue:           collections.NewItem(sb, types.ResidueKey, "residue", sdk.IntValue),
		PruneQueue: collections.NewKeySet(sb, types.PruneQueueKey, "prune_queue",
			collections.TripleKeyCodec(collections.Int64Key, collections.Uint32Key, collections.Uint64Key)),
		PruneDue: collections.NewMap(sb, types.PruneDueKey, "prune_due", streamOption, collections.Int64Value),

		weightSources:      map[types.StreamId]types.WeightSource{},
		integratedHandlers: map[string]integratedHandler{},
		residueSink:        &residueSink{},
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
	k.weightSources[types.STREAM_ID_GROUNDWORKS] = capitalWeightSource{k: k}

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

// --- paying out what the streams have already minted -------------------------
//
// Emission is minted once, when a stream's index advances (see AdvanceIndex), so
// every downstream payment is a transfer out of this module's account rather
// than a second mint. That is what makes this module the only thing that issues
// allocation ERTH, and what makes the supply figure a true one: ERTH the chain
// owes exists from the moment it is owed, not from the moment somebody collects.

// PayOut sends accrued ERTH to an account.
func (k Keeper) PayOut(ctx context.Context, recipient sdk.AccAddress, amount math.Int) error {
	if !amount.IsPositive() {
		return nil
	}
	denom, err := k.HubDenom(ctx)
	if err != nil {
		return err
	}
	return k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipient,
		sdk.NewCoins(sdk.NewCoin(denom, amount)))
}

// PayOutToModule sends accrued ERTH to another module account.
//
// Separate from PayOut because module accounts are on the bank's blocked list:
// SendCoinsFromModuleToAccount consults it and would refuse, which is the same
// wall x/personhood hit with the ANML buyback.
func (k Keeper) PayOutToModule(ctx context.Context, recipientModule string, amount math.Int) error {
	if !amount.IsPositive() {
		return nil
	}
	denom, err := k.HubDenom(ctx)
	if err != nil {
		return err
	}
	return k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, recipientModule,
		sdk.NewCoins(sdk.NewCoin(denom, amount)))
}

// RegisterResidueSink attaches the destination for truncation residue. Called
// once, from app wiring, for the reason spelled out on ResidueSink.
func (k Keeper) RegisterResidueSink(fn func(ctx context.Context, amount math.Int) error) {
	k.residueSink.fn = fn
}

// SweepResidue empties the truncation residue into whatever the sink points at.
//
// Deliberately unconditional on any option's state. AdvanceIndex mints a full
// interval of emission while writing a truncated index, so a hair of every
// interval belongs to no option and would otherwise sit on this module's account
// forever, reading as surplus in the solvency report and drifting further from
// zero every block.
//
// If no sink is registered the dust simply stays put — a chain wired without
// x/distribution is a test fixture, not a broken chain, and stranding a few
// uerth is a better failure than refusing to produce a block.
func (k Keeper) SweepResidue(ctx context.Context) error {
	if k.residueSink.fn == nil {
		return nil
	}
	amount, err := k.GetResidue(ctx)
	if err != nil {
		return err
	}
	if !amount.IsPositive() {
		return nil
	}
	if err := k.residueSink.fn(ctx, amount); err != nil {
		return err
	}
	return k.Residue.Set(ctx, math.ZeroInt())
}
