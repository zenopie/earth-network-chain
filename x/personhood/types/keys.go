package types

import (
	"cosmossdk.io/collections"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
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

	// RegistrationRewardBps is the fraction of the registration-rewards pool paid
	// out on each registration, in basis points (10 bps = 0.1%). The pool stacks
	// from the human allocation stream and decays by this fraction per
	// registration, so each registrant's reward is normalized to the current pool
	// size.
	RegistrationRewardBps = 10

	// EmissionPerSecond is the ERTH emission rate in uerth for this module's
	// pillar (1 ERTH/sec): the ANML buyback-and-burn. The human allocation
	// stream's pillar is emitted by x/allocation.
	EmissionPerSecond = earthtypes.EmissionPerSecondPerPillar

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
)

// The human allocation stream is x/allocation's; this module only supplies its
// weight source (one live registration = one vote) and draws down the
// registration-reward pool.
const (
	// AllocationStream is the stream registered humans vote in.
	AllocationStream = allocationtypes.STREAM_ID_HUMAN

	// RegistrationRewardOptionID is that stream's option #1, whose accrued ERTH
	// is paid out to new registrees and their referrers.
	RegistrationRewardOptionID = allocationtypes.RegistrationRewardOptionID

	// HandlerRegistrationRewards names the integrated handler this module
	// registers for that option. It resolves nothing per block — the pool is
	// drawn down on registration instead.
	HandlerRegistrationRewards = allocationtypes.HandlerRegistrationRewards

	// VoterWeight is the fixed weight of one registered human.
	VoterWeight = allocationtypes.HumanVoterWeight
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_personhood")

// Storage prefixes.
var (
	RegistrationsKey     = collections.NewPrefix("registrations") // nullifier -> Registration
	RegByAddrKey         = collections.NewPrefix("reg_by_addr")   // addr -> nullifier
	RegCountByDscKey     = collections.NewPrefix("regs_by_dsc")
	RegCountByCountryKey = collections.NewPrefix("regs_by_country")
	RegCountKey          = collections.NewPrefix("reg_count")    // uint64
	LastBuybackKey       = collections.NewPrefix("last_buyback") // int64 (unix nanos)
	// RegByRegisteredAt orders registrations by their registration time so the
	// expiry sweep can find the lapsed ones without walking every registration.
	// Keyed on registered-at rather than a precomputed expiry so that a governance
	// change to registration_validity_seconds applies to existing registrations.
	RegByRegisteredAtKey = collections.NewPrefix("reg_by_registered_at") // (registeredAt, nullifier)
)
