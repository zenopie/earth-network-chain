package types

import (
	"fmt"
	"strings"

	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the module name
	ModuleName = "dex"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	// It should be synced with the gov module's name if it is ever changed.
	// See: https://github.com/cosmos/cosmos-sdk/blob/v0.52.0-beta.2/x/gov/types/keys.go#L9
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_dex")

// PoolSeqKey is the prefix for the pool id sequence counter.
var PoolSeqKey = collections.NewPrefix("pool_seq")

// PoolByTokenKey is the prefix for the spoke-token-denom -> pool-id index that
// enforces one pool per token and lets the router find a pool by its token.
var PoolByTokenKey = collections.NewPrefix("pool_by_token")

// Volume-weighted LP reward accounting. Rewards are handed to the dex as a lump
// each block but credited to pools lazily, the same index pattern the allocation
// streams use: the global index advances by reward/totalVolume, and a pool
// collects volume*(index-lastIndex) the next time it is touched.
var (
	// LpRewardIndexKey is the global LP reward index (Int, scaled by 1e18).
	LpRewardIndexKey = collections.NewPrefix("lp_reward_index")
	// LpTotalVolumeKey is the running sum of every pool's stored volume — the
	// denominator the index advances against.
	LpTotalVolumeKey = collections.NewPrefix("lp_total_volume")
	// PoolLpIndexKey is each pool's last settled index (pool id -> Int).
	PoolLpIndexKey = collections.NewPrefix("pool_lp_index")
)

// Genesis liquidity auction — see auction.proto. The auction itself is a
// singleton; bids are keyed by bidder address.
var (
	// LiquidityAuctionKey is the prefix for the auction singleton.
	LiquidityAuctionKey = collections.NewPrefix("liquidity_auction")
	// AuctionBidKey is the prefix for bidder address -> AuctionBid.
	AuctionBidKey = collections.NewPrefix("auction_bid")
)

// PolBurnKey is the prefix for pool id -> PolBurn, the schedules retiring the
// protocol's own liquidity. Entries are deleted when the position reaches zero,
// so the set is empty once every schedule has run out.
var PolBurnKey = collections.NewPrefix("pol_burn")

// LpUnbondingKey is the prefix for in-flight liquidity withdrawals, keyed
// (completion_time, pool_id, address). Completion time leads the key so the
// maturity sweep walks the due prefix in order and stops at the first entry that
// is not ready, instead of scanning every unbonding on the chain.
var LpUnbondingKey = collections.NewPrefix("lp_unbonding")

const (
	// VolumeWindowDays is the number of daily buckets in a pool's rolling volume
	// window used to weight LP reward distribution.
	VolumeWindowDays = 7

	// DefaultLpUnbondingSeconds is how long a liquidity withdrawal waits before
	// it is swept to the provider's wallet: 7 days.
	DefaultLpUnbondingSeconds = 7 * 24 * 60 * 60

	// PolBurnSeconds is how long a protocol-owned liquidity position takes to
	// retire completely: ten Julian years. The genesis ANML/ERTH schedule sets
	// this in the genesis file; the auction pool's schedule is created with it
	// at settlement, so its ten years run from the day its pool opens rather
	// than from block zero.
	PolBurnSeconds = 10 * 36525 * 864 // 315,576,000

	// LpUnbondSweepLimit caps how many matured unbondings are paid out in one
	// block. Each payout settles a pool and moves two coins, so an unbounded
	// sweep would let a large cohort maturing together stall a block. Anything
	// over the cap is picked up by the next block; the ordered key means the
	// backlog drains from the oldest entry first.
	LpUnbondSweepLimit = 50
)

// LPShareDenom returns the LP share coin denom for a given pool id.
func LPShareDenom(poolID uint64) string {
	return fmt.Sprintf("dexlp/%d", poolID)
}

// lpShareDenomPrefix is the shared prefix of every LP share denom.
const lpShareDenomPrefix = "dexlp/"

// IsLPShareDenom reports whether a denom is an LP share rather than an asset.
// The balance invariant needs the distinction: LP shares on the module account
// are claims on reserves already counted, not assets owed to anyone.
func IsLPShareDenom(denom string) bool {
	return strings.HasPrefix(denom, lpShareDenomPrefix)
}
