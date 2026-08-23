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

	// RegistrationRewardPpm is the fraction of the registration-rewards pool paid
	// out on each registration, in parts per million (100 ppm = 0.01%). The pool
	// is pre-funded at genesis with a quarter of the pre-mine, stacks from the
	// human allocation stream, and decays by this fraction per registration, so
	// each registrant's reward is normalized to the current pool size.
	//
	// The draw halves the pool every ln(2)/rate registrations — 6,931 at this
	// rate. Against a quarter of the pre-mine and a $1M auction clear that pays
	// the first registrant and their referrer $50 each, is still $18 a side at
	// the ten-thousandth human, and has distributed 63% of the pool by then and
	// 86% by the twenty-thousandth.
	//
	// Parts per million rather than basis points so the unreferred branch, which
	// halves this, divides exactly. In whole basis points the smallest usable
	// rate was 2, because 1/2 truncates to zero and paid an unreferred registrant
	// nothing without erroring.
	RegistrationRewardPpm = 100

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

	// DefaultRegistrationSweepLimit caps how many registrations BeginBlocker
	// retires per block across every reason for retiring one — lapsed, and
	// belonging to a revoked signer.
	//
	// One budget rather than one per sweep. BeginBlock runs on an infinite gas
	// meter and consumes no block gas, so this number is the only ceiling on
	// that work; splitting it in two would leave the per-block total, which is
	// what actually decides how long a block takes, chosen by nobody. At roughly
	// a dozen store operations per retirement, 100 is a small fraction of a
	// block and drains a large cohort in minutes.
	DefaultRegistrationSweepLimit = 100

	// DefaultDscDailyRegistrationFloor is the minimum daily registration
	// allowance for one Document Signer, whatever the network's size.
	//
	// Sized to be far above any single signer's honest share at launch scale and
	// far below what a compromised key is worth: 1,000 registrations is 1,000
	// ANML a day and 1,000 votes in the democratic pillar, which is a loss worth
	// absorbing while governance revokes, and is nothing like unlimited.
	DefaultDscDailyRegistrationFloor = 1_000

	// DefaultDscDailyRegistrationShareBps lets one signer account for up to 25%
	// of yesterday's network-wide registrations once that is the larger number.
	// A single signer past a quarter of everything is the shape a compromise
	// takes; honest issuance spreads across a country's several active signers.
	DefaultDscDailyRegistrationShareBps = 2_500

	// DefaultCountryDailyRegistrationFloor is the same allowance per issuing
	// country.
	//
	// A thousand a day, not the ten thousand it started at. The floor only
	// matters before the network is big enough for the share term to take over,
	// and at launch scale ten thousand registrations from one country in a day
	// is not adoption — it is the shape a compromised CSCA takes. It also sets
	// how fast the registration-reward pool can drain: at 2 bps the pool halves
	// every 3,466 registrations, so a ten-thousand-a-day country would spend
	// most of the seed inside a week.
	//
	// It is a deferral, not a ban: the counter rolls at midnight UTC and genuine
	// holders retry. Raising it is a governance parameter change, which is the
	// right amount of friction for a number that decides how fast a compromise
	// pays out.
	//
	// NOTE: this now equals DefaultDscDailyRegistrationFloor, so at launch scale
	// the per-signer cap cannot bind before the per-country one does. The signer
	// cap only starts doing independent work once the share term lifts the
	// country cap above it.
	DefaultCountryDailyRegistrationFloor = 1_000

	// DefaultCountryDailyRegistrationShareBps allows one country up to 60% of
	// yesterday's registrations. Deliberately generous: early adoption really can
	// be concentrated in one country, and this bound exists to stop a compromised
	// CSCA minting unlimited signers, not to police which countries register.
	DefaultCountryDailyRegistrationShareBps = 6_000

	// DefaultProofVerificationGas is the gas charged for one UltraHonk proof
	// verification.
	//
	// Derived from the block gas limit rather than from the SDK's signature
	// pricing, because the risk being priced is a block that takes too long to
	// execute, not a fee. BenchmarkVerify (zk/ultrahonk) measures ~5.9ms for the
	// slowest fixture on an Apple M2; call it 10ms to leave room for slower
	// validator hardware. With genesis block_max_gas at 100,000,000, charging
	// 1,000,000 admits at most 100 verifications per block, or about one second
	// of proof CPU against a ~5s block — leaving the rest of the block for
	// everything else.
	//
	// Deriving it the other way produces a far smaller number and the wrong
	// answer: secp256k1 verification measures ~136us on the same machine and the
	// SDK prices it at 1000 gas, which would put this proof at ~43,000 gas and
	// allow ~2,300 verifications — roughly 14 seconds of CPU inside a block that
	// is nominally under its gas limit. The SDK's signature costs are calibrated
	// for transactions carrying one or two signatures, not for an operation a
	// transaction can be made entirely of.
	DefaultProofVerificationGas = 1_000_000

	// DefaultDscVerificationGas is the gas charged for one Document Signer
	// certificate chain verification: DER parsing plus one or two public-key
	// operations. Priced by the same block-limit method at a tenth of the proof
	// charge, which is comfortably above its measured cost and still caps the
	// work at ~1,000 chain verifications per block.
	DefaultDscVerificationGas = 100_000

	// DefaultBuybackTwapWindowSeconds is the minimum age of the price
	// observation the ANML buyback prices against, and so also its cadence:
	// ten minutes, long enough that holding the pool away from its average for a
	// whole window costs materially more than the buyback it would divert.
	DefaultBuybackTwapWindowSeconds = 600

	// DefaultBuybackMaxDeviationBps is how far above the time-weighted average
	// the ANML spot price may sit and still be bought into: 2%.
	//
	// Wide enough to absorb ordinary trading and the small drift from LP reward
	// compounding between observations, tight enough that a sandwich has to move
	// the price further than the buyback is worth in order to profit from it.
	DefaultBuybackMaxDeviationBps = 200

	// DefaultBuybackMaxAccrualSeconds caps a single buyback at one day of
	// emission, bounding the trade that follows a halt or a long run of windows
	// the deviation gate refused.
	DefaultBuybackMaxAccrualSeconds = 24 * 60 * 60

	// BuybackQuoteToleranceBps is the slack between the output the dex quotes for
	// the buyback and the min_out it then demands.
	//
	// The quote is taken in the same block, against the same reserves, through
	// the same code path as the swap, so the two agree exactly and this could be
	// zero. It is not zero because min_out is the last line of defence: if a
	// future change ever lets state move between the quote and the swap, a
	// min_out derived from a stale quote should fail the trade rather than
	// silently accept whatever the pool returns. Ten basis points is too tight
	// for that to be worth attacking and too loose to fail on rounding.
	BuybackQuoteToleranceBps = 10

	// BpsDenominator is the basis-point denominator (100% = 10,000 bps).
	BpsDenominator = 10_000
)

// The human allocation stream is x/allocation's; this module only supplies its
// weight source (one live registration = one vote) and draws down the
// registration-reward pool.
const (
	// AllocationStream is the stream registered humans vote in.
	AllocationStream = allocationtypes.STREAM_ID_CARETAKER

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
	// The buyback's price observation: the dex price accumulator and the block
	// time it was read at. The TWAP the buyback prices against is the difference
	// between this observation and a fresh one, divided by the seconds between
	// them, so the pair has to be stored together and rolled forward together.
	TwapObservationKey = collections.NewPrefix("twap_observation") // math.LegacyDec
	TwapObservedAtKey  = collections.NewPrefix("twap_observed_at") // int64 (unix seconds)
	// RegByRegisteredAt orders registrations by their registration time so the
	// expiry sweep can find the lapsed ones without walking every registration.
	// Keyed on registered-at rather than a precomputed expiry so that a governance
	// change to registration_validity_seconds applies to existing registrations.
	RegByRegisteredAtKey = collections.NewPrefix("reg_by_registered_at") // (registeredAt, nullifier)

	// Daily registration counters behind the per-signer and per-country caps.
	// Each is a self-resetting (day, count) pair — see RateCounter.
	DscRateKey     = collections.NewPrefix("dsc_rate")     // dsc commitment -> RateCounter
	CountryRateKey = collections.NewPrefix("country_rate") // ISO country -> RateCounter
	NetworkRateKey = collections.NewPrefix("network_rate") // RateCounter

	// RegByDsc indexes registrations by the Document Signer that produced them,
	// so retiring a revoked signer's registrations walks only its own prefix.
	// Without it the only way to find them is a scan of every registration on
	// the chain, which is precisely the unbounded work BeginBlock cannot do.
	RegByDscKey = collections.NewPrefix("reg_by_dsc") // (dscKey, nullifier)

	// PendingDscPurge holds the signers whose registrations are still being
	// retired. Revocation adds one; the sweep removes it when its prefix is
	// empty, which is what makes the purge resumable across blocks.
	PendingDscPurgeKey = collections.NewPrefix("pending_dsc_purge") // dscKey
)
