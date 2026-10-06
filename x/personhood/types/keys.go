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
	// halves this, divides exactly (in whole basis points the smallest usable
	// rate would be 2: 1/2 truncates to zero).
	RegistrationRewardPpm = 100

	// EmissionPerSecond is the ERTH emission rate in uerth for this module's
	// pillar (1 ERTH/sec): the ANML buyback-and-burn. The human allocation
	// stream's pillar is emitted by x/allocation.
	EmissionPerSecond = earthtypes.EmissionPerSecondPerPillar

	// DefaultRegistrationValiditySeconds is the default registration lifetime (1 year).
	DefaultRegistrationValiditySeconds = 365 * 24 * 60 * 60

	// MaxRegistrationValiditySeconds bounds how long a registration may go
	// without its Document Signer being checked again. Renewal is when a
	// revoked CSCA's registrations lapse on their own, so an unbounded validity
	// made CSCA revocation never take effect — and past 2^63 the expiry sum
	// wraps negative in isExpired. Three years.
	MaxRegistrationValiditySeconds = 3 * DefaultRegistrationValiditySeconds

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

	// DefaultRegistrationSweepLimit caps the per-block work of BeginBlocker's
	// shared sweeps: retiring registrations (lapsed, or belonging to a revoked
	// signer) and the caretaker, used-binding and handle sweeps.
	//
	// One budget rather than one per sweep. BeginBlock runs on an infinite gas
	// meter and consumes no block gas, so this number is the only ceiling on
	// that work; splitting it in two would leave the per-block total, which is
	// what actually decides how long a block takes, chosen by nobody. Within
	// it, each sweep after the revoked-signer purge has a reserved share
	// (keeper.runSweeps), so no backlog starves the others. At roughly
	// a dozen store operations per retirement, 100 is a small fraction of a
	// block and drains a large cohort in minutes.
	DefaultRegistrationSweepLimit = 100
	// MinRegistrationSweepLimit and MaxRegistrationSweepLimit bound a
	// governance-set limit: at least one per sweep sharing it, and at most
	// what a block can afford to do on an infinite gas meter.
	MinRegistrationSweepLimit = 5
	MaxRegistrationSweepLimit = 10_000

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
	// A thousand a day. The floor only matters before the network is big enough
	// for the share term to take over, and at launch scale ten thousand
	// registrations from one country in a day is not adoption — it is the shape
	// a compromised CSCA takes. It also sets how fast the registration-reward
	// pool can drain: at 100 ppm the pool halves every 6,931 registrations, so a
	// ten-thousand-a-day country would spend most of the seed inside a week.
	//
	// It is a deferral, not a ban: the counter rolls at midnight UTC and genuine
	// holders retry. Raising it is a governance parameter change, which is the
	// right amount of friction for a number that decides how fast a compromise
	// pays out.
	//
	// It equals DefaultDscDailyRegistrationFloor, so at launch scale
	// the per-signer cap cannot bind before the per-country one does. The signer
	// cap only starts doing independent work once the share term lifts the
	// country cap above it.
	DefaultCountryDailyRegistrationFloor = 1_000

	// DefaultCountryDailyRegistrationShareBps allows one country up to 60% of
	// yesterday's registrations. Deliberately generous: early adoption really can
	// be concentrated in one country, and this bound exists to stop a compromised
	// CSCA minting unlimited signers, not to police which countries register.
	DefaultCountryDailyRegistrationShareBps = 6_000

	// DefaultNetworkDailyRegistrationFloor is the least the whole network may
	// register in a day: five country floors. Launch-scale adoption fits under
	// it with room; five compromised roots at once do not fit twice over.
	DefaultNetworkDailyRegistrationFloor = 5_000

	// DefaultNetworkDailyRegistrationGrowthBps lets a day's registrations reach
	// three times the day before's once that is above the floor.
	DefaultNetworkDailyRegistrationGrowthBps = 30_000

	// UnknownCountry is the rate-limit bucket for a signer whose issuer names no
	// country. One shared bucket rather than no bucket, so an empty country does
	// not skip the country cap.
	UnknownCountry = "??"

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
	// Then tripled: the 10ms allowance is for a whole core; a validator may run
	// on a fraction of one (earth-1's does, on Akash), where the same proof is
	// several times slower, and a block of deliberately invalid proofs costs
	// the full verification each. At 3,000,000 a block holds at most 33, about
	// 0.8s even at 24ms apiece.
	//
	// Deriving it the other way produces a far smaller number and the wrong
	// answer: secp256k1 verification measures ~136us on the same machine and the
	// SDK prices it at 1000 gas, which would put this proof at ~43,000 gas and
	// allow ~2,300 verifications — roughly 14 seconds of CPU inside a block that
	// is nominally under its gas limit. The SDK's signature costs are calibrated
	// for transactions carrying one or two signatures, not for an operation a
	// transaction can be made entirely of.
	DefaultProofVerificationGas = 3_000_000

	// DefaultDscVerificationGas is the gas charged for one Document Signer
	// certificate chain verification: DER parsing plus one or two public-key
	// operations, and the Poseidon2 commitment over the key. Priced by the same
	// block-limit method at a tenth of the proof charge.
	//
	// Tripled with the proof charge. The worst case is not one
	// signature check but MaxIssuerCandidates of them, against trust-store keys
	// as large as governance has admitted — brainpool512 and 4096-bit RSA
	// among them — plus a commitment that absorbs one field element per key
	// byte. At 300,000 a block holds ~330.
	DefaultDscVerificationGas = 300_000

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

	// DefaultBuybackMaxTradeSeconds caps one buyback trade at an hour of
	// emission (3600 ERTH). A larger backlog is bought over successive
	// windows rather than in one order sized for a sandwich.
	DefaultBuybackMaxTradeSeconds = 60 * 60

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

	// DefaultIdentityRootWindowSeconds is how long a superseded identity-tree
	// root stays a membership anchor: an hour, enough for a wallet to sync,
	// prove and land a tx, and short enough that a zeroed leaf (expired,
	// revoked, switched) stops proving soon after.
	DefaultIdentityRootWindowSeconds = 60 * 60

	// DefaultCaretakerVoteSeconds (R) is how long a caretaker split counts
	// once cast: a year. Its owner renews it by casting again.
	DefaultCaretakerVoteSeconds = 365 * 24 * 60 * 60

	// SecondsPerDay is the UTC day the ANML claim is keyed by.
	SecondsPerDay = 86400

	// IdentityRootPruneLimit caps how many expired identity roots EndBlock
	// deletes per block. At most one root is recorded per block.
	IdentityRootPruneLimit = 20

	// ClaimNullifierPruneLimit caps how many stale claim nullifiers one block
	// deletes. A day's claims are at most one per registration; the backlog
	// drains over following blocks.
	ClaimNullifierPruneLimit = 1000

	// CaretakerSweepLimit is the lapsed caretaker leases one block retires on
	// a budget of their own (BeginBlocker), before x/allocation settles the
	// stream. A lease is filed by a private action (one membership proof) and
	// lapses a fixed lease length later; a block holds a few dozen private
	// actions (x/shielded max_private_actions_per_block, default 32), so no
	// block can see this many lapse unless a lowered lease length bunches two
	// cohorts together, and even then the remainder drains next block.
	CaretakerSweepLimit = 1000

	// MaxIdentityLeavesQuery caps one IdentityLeaves page.
	MaxIdentityLeavesQuery = 1000
)

// The caretaker allocation stream is x/allocation's; this module files its
// anonymous voters (by caretaker nullifier, one fixed weight each) and draws
// down the registration-reward pool.
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

	// VoterWeight is the fixed weight of one caretaker split.
	VoterWeight = allocationtypes.HumanVoterWeight
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_personhood")

// Storage prefixes.
var (
	RegistrationsKey     = collections.NewPrefix("registrations") // nullifier -> Registration
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

	// The identity tree (depth 32, updatable): its non-empty nodes, keyed
	// (level, index), and its append cursor.
	IdentityNodesKey = collections.NewPrefix("identity_nodes")
	IdentitySizeKey  = collections.NewPrefix("identity_size")
	// Identity roots recorded at the end of each block that moved the tree,
	// by root and by (time, root) for pruning; and the latest, which never
	// expires.
	IdentityRootsKey       = collections.NewPrefix("identity_roots")
	IdentityRootsByTimeKey = collections.NewPrefix("identity_time_index")
	LatestIdentityRootKey  = collections.NewPrefix("latest_identity_root")

	// ClaimNullifiersKey is (day, membership nullifier) for every ANML claim
	// of the last two days. Older days are pruned: a claim for them is
	// refused by its day anyway.
	ClaimNullifiersKey = collections.NewPrefix("claim_nullifiers")

	// CaretakerVotesKey maps a caretaker nullifier to when its split lapses;
	// CaretakerExpiryKey orders them by that for the sweep; CaretakerCountKey
	// counts them.
	CaretakerVotesKey  = collections.NewPrefix("caretaker_votes")
	CaretakerExpiryKey = collections.NewPrefix("caretaker_expiry")
	CaretakerCountKey  = collections.NewPrefix("caretaker_count")

	// HandlesKey maps a handle to its Handle record; HandleByNfKey is the
	// reverse index (nullifier -> handle); HandleReleaseKey orders records
	// by (expires_at, handle) for the sweep, which releases a handle once its
	// renewal period (handle_renewal_seconds) has passed too.
	HandlesKey       = collections.NewPrefix("handles")
	HandleByNfKey    = collections.NewPrefix("handle_by_nf")
	HandleReleaseKey = collections.NewPrefix("handle_release")
	// HandleLeaseMaxKey: the longest handle lease ever in force.
	HandleLeaseMaxKey = collections.NewPrefix("handle_lease_max")
	// PassportsSeenKey: passport nullifiers ever registered.
	PassportsSeenKey = collections.NewPrefix("passports_seen")

	// UsedBindingsKey maps a landed registration's binding to when it may be
	// forgotten; UsedBindingExpiryKey orders them by that for the sweep.
	UsedBindingsKey      = collections.NewPrefix("used_bindings")
	UsedBindingExpiryKey = collections.NewPrefix("used_binding_expiry")

	// LeaseHoldKey: types.LeaseHold, the lowered-lease-length hold.
	LeaseHoldKey = collections.NewPrefix("lease_hold")

	// SweepRetryKey maps a registration the expiry or purge sweep failed to
	// retire to when the sweeps may try it again (audit 6 B6-5). Not
	// exported: an import retries everything.
	SweepRetryKey = collections.NewPrefix("sweep_retry")
)

// SweepRetrySeconds is how long a registration the sweeps failed to retire
// is passed over before they try it again: a day.
const SweepRetrySeconds = 24 * 60 * 60

// SweepScanFactor bounds how many index entries a sweep may pass over (ones
// waiting out SweepRetrySeconds) per entry of its budget.
const SweepScanFactor = 8

// UsedBindingGraceSeconds is added to current_date_max_skew_seconds for how
// long a landed registration's binding is refused: current_date is a day
// granular, so a proof dated today stays inside the skew up to a day longer
// than the skew alone.
const UsedBindingGraceSeconds = 24 * 60 * 60

// MaxCurrentDateMaxSkewSeconds is the largest current_date_max_skew_seconds
// governance may set (a year). A used binding is held for it whatever the
// skew in force, so raising the skew never reopens a replay.
const MaxCurrentDateMaxSkewSeconds = 365 * SecondsPerDay

// ActivationMarginSeconds is how long before a ballot opens (or, for a lease,
// before now less the lease length) a member's identity must have been
// activated to take part: the largest identity_root_window_seconds governance
// may set. A zeroed leaf keeps proving for at most one root window, so with
// the margin at its maximum no change of the window, either way and at any
// time, lets an identity and the one it switched to both take part (an old
// root never outlives the margin). The window parameter itself only governs
// how long a superseded root stays an anchor.
const ActivationMarginSeconds = SecondsPerDay
