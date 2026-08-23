package types

import (
	"cosmossdk.io/collections"

	earthtypes "github.com/earth-network/earth/x/earth/types"
)

const (
	// ModuleName defines the module name
	ModuleName = "allocation"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_allocation")

// Stream state. Every prefix below is keyed by the stream id first, so the two
// streams share the engine and the code but never a byte of state: resetting,
// resyncing or re-indexing one cannot reach into the other.
var (
	OptionsKey     = collections.NewPrefix("options")      // (stream, id) -> AllocationOption
	OptionSeqKey   = collections.NewPrefix("option_seq")   // stream -> uint64
	VotersKey      = collections.NewPrefix("voters")       // (stream, addr) -> Voter
	RewardIndexKey = collections.NewPrefix("reward_index") // stream -> Int
	TotalWeightKey = collections.NewPrefix("total_weight") // stream -> Int
	// SummedWeightKey is the running sum of the stream's options' allocations,
	// maintained on every option write by keeper.setOption.
	//
	// It exists so the per-block check does not have to walk the options to
	// learn what they add up to. The two figures are deliberately maintained at
	// different sites — TotalWeight moves by a voter's whole weight in
	// resyncVoter, this moves by the amount each option actually took — so
	// comparing them is a real check and not a number agreeing with itself.
	SummedWeightKey      = collections.NewPrefix("summed_weight")      // stream -> Int
	LastUpkeepKey        = collections.NewPrefix("last_upkeep")        // stream -> int64 (unix nanos)
	IntegratedOptionsKey = collections.NewPrefix("integrated_options") // (stream, id)
	// PruneQueueKey orders dead options by when they may be removed, so the
	// sweep can stop at the first entry that is not due yet instead of looking
	// at the rest. Keyed by time first for exactly that reason.
	PruneQueueKey = collections.NewPrefix("prune_queue") // (due unix, stream, id)
	// PruneDueKey is the reverse lookup, so an option that comes back to life
	// can cancel its own removal without searching the queue for itself.
	PruneDueKey = collections.NewPrefix("prune_due") // (stream, id) -> due unix

	// SummedAccruedKey is the running sum of every option's Accumulated, across
	// both streams, maintained by keeper.setOption. It exists so the solvency
	// check does not have to walk the options to learn what they add up to —
	// adding an ADDRESS option is permissionless, so the per-block cost must not
	// grow with the option count.
	SummedAccruedKey = collections.NewPrefix("summed_accrued") // Int

	// ResidueKey is emission that was minted but that no option can collect:
	// what a stream's index truncation leaves over. Held until it is swept to
	// the community pool. Global rather than per stream — it is dust either way,
	// and nothing about it is worth knowing per stream.
	ResidueKey = collections.NewPrefix("residue") // Int

	// EpochKey is the per-stream allocation epoch. Bumped by a governance reset;
	// votes recorded under an older epoch carry no live weight. Per stream rather
	// than global, so resetting one slate leaves the other one standing.
	EpochKey = collections.NewPrefix("epoch") // stream -> uint64
)

const (
	// RegistrationRewardOptionID is the id of the human stream's genesis
	// "registration rewards" option (#1). Ids are per stream, so this and
	// LPRewardsOptionID being the same number is not a collision.
	RegistrationRewardOptionID = 1

	// LPRewardsOptionID is the id of the capital stream's genesis
	// "volume-weighted LP rewards" option (#1).
	LPRewardsOptionID = 1

	// CommunityPoolOptionID is the id of the capital stream's genesis
	// "emergency fund" option (#2), which pays into the SDK community pool.
	CommunityPoolOptionID = 2

	// EmissionPerSecond is what each stream emits, in uerth (1 ERTH/sec). Both
	// streams run at the same rate: one pillar of the four is directed by humans
	// and one by stake.
	EmissionPerSecond = earthtypes.EmissionPerSecondPerPillar

	// PpmDenominator is the parts-per-million denominator DrawFromOption divides
	// by (100% = 1,000,000).
	PpmDenominator = 1_000_000

	// DefaultAddressOptionFee is the ERTH (uerth) burned to add an ADDRESS option.
	DefaultAddressOptionFee = 1_000_000

	// HandlerLPRewards compounds ERTH into the dex pools (capital stream).
	HandlerLPRewards = "lp_rewards"

	// HandlerRegistrationRewards accrues a pool paid out on registration
	// (human stream). It resolves nothing per block; x/personhood draws it down.
	HandlerRegistrationRewards = "registration_rewards"

	// HandlerCommunityPool credits the accrued ERTH to the SDK community pool
	// (capital stream) — the emergency fund. It has to be INTEGRATED rather than
	// an ADDRESS option anyone could add: the community pool is x/distribution's
	// FeePool, not a wallet, so a plain transfer to the distribution module
	// account would raise its balance without crediting the pool, and the coins
	// could never be spent by a MsgCommunityPoolSpend. (The account is blocked
	// besides, so the claim would not even land.) Only
	// distribution.FundCommunityPool moves the coins and the FeePool together.
	HandlerCommunityPool = "community_pool"

	// HumanVoterWeight is the fixed weight of one registered human. Every human
	// carries the same weight, which is what makes this stream one-human-one-vote.
	HumanVoterWeight = 100

	// OptionIdleGrace is how long a dead option is kept before it is removed:
	// thirty days without a single voter.
	//
	// Rewards it had earned and nobody claimed go with it. Anyone may trigger a
	// claim on an ADDRESS option — the payout goes to the recipient whoever
	// sends it — so thirty days is a long time for a live recipient to leave
	// money on the table, and the alternative is that a sliver of unclaimed dust
	// makes a row immortal.
	//
	// Adding an option is permissionless and its fee is paid once, so without a
	// way out every option ever added is a row the chain stores forever, whether
	// or not a single voter ever pointed at it. The fee makes that expensive
	// rather than free, which is why this is a grace period and not a tight one:
	// the goal is to let genuinely dead entries go, not to police them.
	//
	// A constant rather than a parameter. It is a housekeeping interval, not a
	// lever anyone should need to pull under pressure, and a governance vote can
	// change it by upgrade like any other constant here.
	OptionIdleGrace = 30 * 24 * 60 * 60 // seconds

	// OptionPruneSweepLimit caps how many options are removed in one block.
	//
	// Removals are cheap individually, but the queue can hold an arbitrary
	// number of entries that fall due together — options added in a burst come
	// due in a burst thirty days later. The remainder is not stranded: the next
	// block resumes from the oldest entry. Counted in the per-block budget, see
	// app/block_budget_test.go.
	OptionPruneSweepLimit = 20

	// MaxOptionsPageSize caps how many options one Options query returns.
	//
	// The query is free to call and the option table is permissionless, so
	// without a ceiling one request could be made to read the whole of it. A
	// caller asking for more than this gets this many; a caller asking for none
	// gets the SDK default. Walking the table is still possible page by page,
	// but each page is a separate request that a node can meter and rate-limit.
	MaxOptionsPageSize = 100

	// MaxDescriptionLen caps an option's description, in bytes.
	//
	// Bytes rather than characters, because bytes are what state costs. The
	// description is a label — enough for "Fund the docs site", not for a
	// manifesto. Anything longer belongs off chain, keyed by option id.
	//
	// It needs a hard bound because adding an ADDRESS option is permissionless
	// and its cost is charged once: the fee is burned, the gas is paid at the
	// block that stores it, and after that every node decodes the record in
	// every EndBlock forever, since CheckStreamWeight walks every option in the
	// stream. Unbounded, roughly one ERTH of fee plus a fifth of one in gas buys
	// a megabyte of description that is re-read for the life of the chain.
	MaxDescriptionLen = 256

	// MaxVoterOptions caps how many options one voter may split across.
	//
	// This has to be a hard bound rather than a convention, because resyncing a
	// voter is reachable from BeginBlock: x/personhood's expiry sweep clears a
	// lapsed human's vote, which unwinds their stored split one option at a time,
	// and nobody pays gas for that. Uncapped, a voter could store thousands of
	// entries and hand the chain the bill when their registration lapsed —
	// multiplied by the sweep limit's worth of registrations in the same block.
	MaxVoterOptions = 20
)

// Streams is every valid stream id, in id order. Used by genesis and BeginBlock,
// which have to touch both streams and must do so in a fixed order.
var Streams = []StreamId{STREAM_ID_CARETAKER, STREAM_ID_GROUNDWORKS}

// ValidateDescription bounds an option's description. Shared by the message
// server and by genesis validation, so an import cannot carry what a message
// could not have created.
func ValidateDescription(description string) error {
	if len(description) > MaxDescriptionLen {
		return ErrDescriptionTooLong.Wrapf("%d bytes exceeds the maximum of %d",
			len(description), MaxDescriptionLen)
	}
	return nil
}

// ValidateStreamId rejects a message or a genesis entry that names no stream or
// an unknown one. It lives here rather than in the keeper so genesis validation
// can reach it; keeper.ValidateStream delegates to it.
func ValidateStreamId(stream StreamId) error {
	for _, s := range Streams {
		if s == stream {
			return nil
		}
	}
	return ErrUnknownStream.Wrapf("%s", stream)
}
