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
