package types

import (
	"cosmossdk.io/collections"

	earthtypes "github.com/earth-network/earth/x/earth/types"
)

const (
	// ModuleName defines the module name
	ModuleName = "deflation"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_deflation")

// Allocation (stake-weighted, plutocratic) reward-stream storage prefixes.
var (
	AllocationOptionsKey = collections.NewPrefix("allocation_options")
	AllocationSeqKey     = collections.NewPrefix("allocation_seq")
	VotersKey            = collections.NewPrefix("voters")
	RewardIndexKey       = collections.NewPrefix("reward_index")
	TotalWeightKey       = collections.NewPrefix("total_weight")
	LastAllocUpkeepKey   = collections.NewPrefix("last_alloc_upkeep")
	IntegratedOptionsKey = collections.NewPrefix("integrated_options")
	// AllocationEpochKey is the allocation epoch. Bumped by a governance reset;
	// votes recorded under an older epoch carry no live weight.
	AllocationEpochKey = collections.NewPrefix("allocation_epoch") // uint64


)

const (
	// LPRewardsOptionID is the id of the genesis "volume-weighted LP rewards"
	// allocation option (option #1).
	LPRewardsOptionID = 1

	// EmissionPerSecond is the stake-weighted allocation emission rate in uerth
	// (1 ERTH/sec, micro-units).
	EmissionPerSecond = earthtypes.EmissionPerSecondPerPillar

	// DefaultAddressOptionFee is the ERTH (uerth) burned to add an ADDRESS option.
	DefaultAddressOptionFee = 1_000_000

	// HandlerLPRewards is the integrated handler that compounds ERTH into dex pools.
	HandlerLPRewards = "lp_rewards"

	// MaxVoterOptions caps how many options one voter may split across.
	//
	// Resyncing a voter walks their stored split option by option, twice — once to
	// subtract the old weight and once to add the new. Today every resync is
	// triggered by a transaction whose sender pays gas, but the staking hooks make
	// it reachable from paths the chain drives itself, so the cost has to be
	// bounded rather than merely paid for.
	MaxVoterOptions = 20
)
