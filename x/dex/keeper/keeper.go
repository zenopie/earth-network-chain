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

	"github.com/earth-network/earth/x/dex/types"
)

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
	Pool          collections.Map[uint64, types.Pool]
	PoolSeq       collections.Sequence
	// PoolByToken indexes the spoke token denom to its pool id (one pool per token).
	PoolByToken collections.Map[string, uint64]

	// Lazy LP reward accounting — see lp_rewards.go.
	LpRewardIndex collections.Item[math.Int]
	LpTotalVolume collections.Item[math.Int]
	PoolLpIndex   collections.Map[uint64, math.Int]
	// PendingLpRewards is ERTH paid in by the allocation stream and not yet
	// settled into a pool reserve. Counted as an obligation. See invariants.go.
	PendingLpRewards collections.Item[math.Int]

	// Volume is stored scaled by VolumeIndex rather than decayed in place; the
	// stale queue retires the weight of pools that stop trading. See lp_rewards.go.
	VolumeIndex    collections.Item[math.Int]
	VolumeIndexDay collections.Item[uint64]
	PoolStaleQueue collections.KeySet[collections.Pair[int64, uint64]]
	PoolStaleDue   collections.Map[uint64, int64]

	// In-flight liquidity withdrawals, keyed (completion_time, pool_id, address)
	// so the maturity sweep can walk them in due order — see lp_unbonding.go.
	LpUnbondings collections.Map[collections.Triple[int64, uint64, []byte], types.LpUnbonding]

	// Genesis liquidity auction — see auction.go. The auction is a singleton;
	// bids are keyed by bidder address bytes.
	LiquidityAuction collections.Item[types.LiquidityAuction]
	AuctionBids      collections.Map[[]byte, types.AuctionBid]

	// Protocol-owned liquidity retirement, keyed by pool id — see pol_burn.go.
	PolBurns collections.Map[uint64, types.PolBurn]

	// Bounded solvency accounting — see solvency.go.
	TotalPoolErth  collections.Item[math.Int]
	DirtyPools     collections.KeySet[uint64]
	SolvencyCursor collections.Item[uint64]

	// Time-weighted price accumulator, keyed by pool id — see twap.go.
	PriceCumulative collections.Map[uint64, math.LegacyDec]
	PriceObservedAt collections.Map[uint64, int64]
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

	k := Keeper{
		storeService:  storeService,
		cdc:           cdc,
		addressCodec:  addressCodec,
		authority:     authority,
		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,

		Params:      collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Pool:        collections.NewMap(sb, types.PoolKey, "pool", collections.Uint64Key, codec.CollValue[types.Pool](cdc)),
		PoolSeq:     collections.NewSequence(sb, types.PoolSeqKey, "pool_seq"),
		PoolByToken: collections.NewMap(sb, types.PoolByTokenKey, "pool_by_token", collections.StringKey, collections.Uint64Value),

		LpRewardIndex: collections.NewItem(sb, types.LpRewardIndexKey, "lp_reward_index", sdk.IntValue),
		LpTotalVolume: collections.NewItem(sb, types.LpTotalVolumeKey, "lp_total_volume", sdk.IntValue),
		PoolLpIndex:   collections.NewMap(sb, types.PoolLpIndexKey, "pool_lp_index", collections.Uint64Key, sdk.IntValue),

		PendingLpRewards: collections.NewItem(sb, types.PendingLpRewardsKey, "pending_lp_rewards", sdk.IntValue),

		VolumeIndex:    collections.NewItem(sb, types.VolumeIndexKey, "volume_index", sdk.IntValue),
		VolumeIndexDay: collections.NewItem(sb, types.VolumeIndexDayKey, "volume_index_day", collections.Uint64Value),
		PoolStaleQueue: collections.NewKeySet(sb, types.PoolStaleQueueKey, "pool_stale_queue",
			collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),
		PoolStaleDue: collections.NewMap(sb, types.PoolStaleDueKey, "pool_stale_due", collections.Uint64Key, collections.Int64Value),

		LpUnbondings: collections.NewMap(
			sb, types.LpUnbondingKey, "lp_unbondings",
			collections.TripleKeyCodec(collections.Int64Key, collections.Uint64Key, collections.BytesKey),
			codec.CollValue[types.LpUnbonding](cdc),
		),

		LiquidityAuction: collections.NewItem(
			sb, types.LiquidityAuctionKey, "liquidity_auction",
			codec.CollValue[types.LiquidityAuction](cdc),
		),
		AuctionBids: collections.NewMap(
			sb, types.AuctionBidKey, "auction_bids",
			collections.BytesKey, codec.CollValue[types.AuctionBid](cdc),
		),

		TotalPoolErth:  collections.NewItem(sb, types.TotalPoolErthKey, "total_pool_erth", sdk.IntValue),
		DirtyPools:     collections.NewKeySet(sb, types.DirtyPoolsKey, "dirty_pools", collections.Uint64Key),
		SolvencyCursor: collections.NewItem(sb, types.SolvencyCursorKey, "solvency_cursor", collections.Uint64Value),

		PriceCumulative: collections.NewMap(sb, types.PriceCumulativeKey, "price_cumulative", collections.Uint64Key, sdk.LegacyDecValue),
		PriceObservedAt: collections.NewMap(sb, types.PriceObservedAtKey, "price_observed_at", collections.Uint64Key, collections.Int64Value),

		PolBurns: collections.NewMap(
			sb, types.PolBurnKey, "pol_burns",
			collections.Uint64Key, codec.CollValue[types.PolBurn](cdc),
		),
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

// HubDenom returns the hub asset denom (the staking coin, ERTH) that every pool
// pairs against.
func (k Keeper) HubDenom(ctx context.Context) (string, error) {
	return k.stakingKeeper.BondDenom(ctx)
}

// HasPoolForToken reports whether a pool exists for the given spoke token denom.
func (k Keeper) HasPoolForToken(ctx context.Context, tokenDenom string) (bool, error) {
	return k.PoolByToken.Has(ctx, tokenDenom)
}

// PoolForToken returns the pool paired with the given spoke token denom.
func (k Keeper) PoolForToken(ctx context.Context, tokenDenom string) (types.Pool, error) {
	poolID, err := k.PoolByToken.Get(ctx, tokenDenom)
	if err != nil {
		return types.Pool{}, err
	}
	return k.Pool.Get(ctx, poolID)
}
