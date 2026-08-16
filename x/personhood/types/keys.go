package types

import (
	"cosmossdk.io/collections"

	earthtypes "github.com/earth-network/earth/x/earth/types"
)

const (
	// ModuleName defines the module name
	ModuleName = "personhood"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	GovModuleName = "gov"

	// AnmlDenom is the ANML token denom (micro-units; 1 ANML = 1e6 uanml).
	AnmlDenom = "uanml"

	// OneAnml is one ANML in uanml, minted per daily claim / registration.
	OneAnml = 1_000_000

	// RegistrationRewardOptionID is the genesis democratic option (#1) whose ERTH
	// is paid out to new registrees and their referrers.
	RegistrationRewardOptionID = 1

	// VoterWeight is the fixed per-human democratic vote weight (one-human-one-vote).
	VoterWeight = 100

	// RegistrationRewardBps is the fraction of the registration-rewards pool paid
	// out on each registration, in basis points (10 bps = 0.1%). The pool stacks
	// from the democratic stream and decays by this fraction per registration, so
	// each registrant's reward is normalized to the current pool size.
	RegistrationRewardBps = 10

	// EmissionPerSecond is the ERTH emission rate in uerth for each democratic
	// stream (1 ERTH/sec): buyback-and-burn, and democratic allocations.
	EmissionPerSecond = earthtypes.EmissionPerSecondPerPillar

	// DefaultAddressOptionFee is the ERTH (uerth) burned to add an ADDRESS option.
	DefaultAddressOptionFee = 1_000_000

	// HandlerRegistrationRewards is the integrated handler for the registration-
	// reward pool (accrues each block; paid out on registration).
	HandlerRegistrationRewards = "registration_rewards"

	// DefaultRegistrationValiditySeconds is the default registration lifetime (1 year).
	DefaultRegistrationValiditySeconds = 365 * 24 * 60 * 60

	// DefaultCurrentDateMaxSkewSeconds bounds how far the prover-supplied
	// current_date may sit from block time (48h).
	//
	// The circuit encodes current_date as YYMMDD, so it resolves to midnight UTC
	// of that day: a proof generated at 23:00 is already ~24h behind block time
	// through the encoding alone. The second day absorbs device clock drift, a
	// device whose local date is a day off UTC, and the gap between proving on
	// the phone and the transaction landing in a block. Two days of slack is
	// nowhere near the years of backdating needed to revive an expired passport.
	DefaultCurrentDateMaxSkewSeconds = 48 * 60 * 60

	// DefaultExpirySweepLimit caps how many lapsed registrations BeginBlocker
	// retires in one block, so a cohort that all registered together cannot land
	// an unbounded sweep on a single block. Overridable via params.
	DefaultExpirySweepLimit = 100

	// MaxVoterOptions caps how many options one voter may split across.
	//
	// This has to be a hard bound rather than a convention, because resyncDemVoter
	// is reachable from BeginBlock: the expiry sweep calls removeRegistration,
	// which unwinds the lapsed voter's stored split one option at a time, and
	// nobody pays gas for that. Uncapped, a human could store thousands of entries
	// and hand the chain the bill when their registration lapsed — multiplied by
	// DefaultExpirySweepLimit registrations in the same block.
	MaxVoterOptions = 20
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_personhood")

// Storage prefixes.
var (
	RegistrationsKey        = collections.NewPrefix("registrations") // nullifier -> Registration
	RegByAddrKey            = collections.NewPrefix("reg_by_addr")   // addr -> nullifier
	RegCountByDscKey        = collections.NewPrefix("regs_by_dsc")
	RegCountByCountryKey    = collections.NewPrefix("regs_by_country")
	RegCountKey             = collections.NewPrefix("reg_count")        // uint64
	DemOptionsKey           = collections.NewPrefix("dem_options")      // id -> DemocraticOption
	DemSeqKey               = collections.NewPrefix("dem_seq")          // sequence
	DemVotersKey            = collections.NewPrefix("dem_voters")       // addr -> DemocraticVoter
	DemRewardIndexKey       = collections.NewPrefix("dem_reward_index") // Int
	DemTotalWeightKey       = collections.NewPrefix("dem_total_weight") // Int
	DemLastUpkeepKey        = collections.NewPrefix("dem_last_upkeep")  // int64 (unix nanos)
	LastBuybackKey          = collections.NewPrefix("last_buyback")     // int64 (unix nanos)
	DemIntegratedOptionsKey = collections.NewPrefix("dem_integrated_options")
	// RegByRegisteredAt orders registrations by their registration time so the
	// expiry sweep can find the lapsed ones without walking every registration.
	// Keyed on registered-at rather than a precomputed expiry so that a governance
	// change to registration_validity_seconds applies to existing registrations.
	RegByRegisteredAtKey = collections.NewPrefix("reg_by_registered_at") // (registeredAt, nullifier)
	// DemEpochKey is the democratic allocation epoch. Bumped by a governance
	// reset; votes recorded under an older epoch carry no live weight.
	DemEpochKey = collections.NewPrefix("dem_epoch") // uint64
)
