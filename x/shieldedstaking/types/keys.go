// Package types defines private staking: the module as the sole non-self
// delegator, derth/<valoper> owner-locked stake notes in the module's own
// stake note tree, epochs, undelegation payouts, stake votes and Groundworks
// votes.
package types

import (
	"strings"
	"time"

	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the module name. Its account is the delegator.
	ModuleName = "shieldedstaking"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency.
	GovModuleName = "gov"

	// BondDenom is the staking coin; the module accepts nothing else.
	BondDenom = "uerth"

	// DerthPrefix starts this module's stake denoms. They name stake note
	// assets (zk/privacy.AssetID of the denom) and are never coins,
	// shielded-pool assets or dex tokens.
	DerthPrefix = "derth/"

	// MaxOptionsPerVote bounds a stake vote's weighted options (x/gov has four).
	MaxOptionsPerVote = 4

	// EpochValidatorLimit caps how many validator books one block processes.
	// An epoch end starts a sweep over every book, in key order from a
	// persistent cursor; it goes on in the following blocks, this many books a
	// block, until it reaches the last book (EpochSweep). Every book is
	// processed within ceil(books / EpochValidatorLimit) blocks of the epoch
	// end, whatever the books' keys.
	EpochValidatorLimit = 200

	// InvariantBookLimit bounds the epoch end's invariant check: with more
	// books, unbond records and Groundworks votes than this it is skipped (and an event says so)
	// rather than walk them all in EndBlock. Tests and genesis run it in full.
	InvariantBookLimit = 1_000

	// CheckpointPruneLimit caps how many dead supply checkpoints one block
	// deletes.
	CheckpointPruneLimit = 500

	// EscrowRetireLimit caps how many retired operators' escrows one block
	// releases.
	EscrowRetireLimit = 50

	// EscrowRetryDelay is how far a failed retirement release moves back in
	// the queue, behind the entries due now, so one that keeps failing
	// cannot hold the head of the queue.
	EscrowRetryDelay = 24 * time.Hour

	// SnapshotSweepLimit caps how many finished proposals one block forgets.
	SnapshotSweepLimit = 20

	// UnbondPayoutSweepLimit caps how many undelegation payouts one block
	// tries (due retries first, then matured records' payouts in order);
	// UnbondPayoutNoteBudget caps the notes one block mints for them. A
	// payout whose notes would pass the budget waits for the next block,
	// unless it is the block's first (one payout mints at most
	// MaxSplitNotes = 128).
	UnbondPayoutSweepLimit = 50
	UnbondPayoutNoteBudget = 256

	// UnbondPayoutRetryBaseSeconds and UnbondPayoutRetryMaxShift: a payout
	// that fails is retried, never dropped, at now + base << min(attempts-1,
	// max shift): 1h, 2h, 4h, ... capped at 256h (as x/dex's LP payouts).
	UnbondPayoutRetryBaseSeconds = 3600
	UnbondPayoutRetryMaxShift    = 8

	// ValidatorVoterPrefix starts a validator's Groundworks voter key in
	// x/allocation (ValidatorVoterKey).
	ValidatorVoterPrefix = "gwpos/"
)

// Storage prefixes.
var (
	ParamsKey        = collections.NewPrefix(0)
	EpochKey         = collections.NewPrefix(1)
	ValidatorsKey    = collections.NewPrefix(2)
	UnbondRecordsKey = collections.NewPrefix(3)
	MaturityQueueKey = collections.NewPrefix(4)
	// GwVotesKey holds the Groundworks votes by id, GwVoteSeqKey the id
	// sequence (groundworks.go). 5 and 6 held the retired positions and their
	// sequence; the upgrade that retired them cleared both.
	GwVotesKey        = collections.NewPrefix(5)
	GwVoteSeqKey      = collections.NewPrefix(6)
	SnapshotsKey      = collections.NewPrefix(7)
	VotesKey          = collections.NewPrefix(8)
	TalliesKey        = collections.NewPrefix(9)
	SnapshotExpiryKey = collections.NewPrefix(10)
	// 11 is reserved (was PositionsByValKey; cleared by the upgrade).
	PendingRecordsKey = collections.NewPrefix(12)
	// SlashedValidatorsKey holds the validators slashed in the current block,
	// whose epoch rate and Groundworks votes EndBlock re-weighs. Emptied every
	// EndBlock; never exported.
	SlashedValidatorsKey = collections.NewPrefix(13)
	// The stake note tree: its nodes and size, its nullifier set, and its
	// recorded roots (by root, and by time for pruning).
	StakeTreeNodesKey = collections.NewPrefix(14)
	StakeTreeSizeKey  = collections.NewPrefix(15)
	// StakeNullifiersKey maps each spent stake nullifier to its leaf index in
	// the stake nullifier tree (nf_tree.go), ordered by value.
	StakeNullifiersKey  = collections.NewPrefix(16)
	StakeRootsKey       = collections.NewPrefix(17)
	StakeRootsByTimeKey = collections.NewPrefix(18)
	StakeLatestRootKey  = collections.NewPrefix(19)

	// RewardEscrowsKey maps each validator's reward escrow account to the
	// validator (escrow.go).
	RewardEscrowsKey = collections.NewPrefix(20)

	// Lazy per-validator gov snapshots (votes.go): the last snapshot sequence
	// issued, each book's supply checkpoints by (validator, seq) and by
	// (seq, validator) for pruning, and the open snapshots by (seq, proposal).
	SnapshotSeqKey       = collections.NewPrefix(21)
	SupplyCheckpointsKey = collections.NewPrefix(22)
	CheckpointsBySeqKey  = collections.NewPrefix(23)
	SnapshotsBySeqKey    = collections.NewPrefix(24)
	// EpochSweepKey is the epoch-end sweep's state (epoch.go).
	EpochSweepKey = collections.NewPrefix(25)
	// 26 is reserved (was PositionCountKey).
	// RetiringEscrowsKey schedules (time, validator) the release of a reward
	// escrow whose operator removed its whole self-bond (escrow.go);
	// PendingReleasesKey holds removed validators whose release failed.
	RetiringEscrowsKey = collections.NewPrefix(27)
	PendingReleasesKey = collections.NewPrefix(28)
	// Groundworks totals (groundworks.go): per (validator, option) the sum of
	// derth x percent over the validator's live votes, and per validator
	// the Groundworks allocation epoch those totals belong to.
	GwTotalsKey = collections.NewPrefix(29)
	GwEpochKey  = collections.NewPrefix(30)
	// PendingReleaseCursorKey is the last PendingReleases entry the epoch
	// end retried: retries rotate through the set, so entries that keep
	// failing cannot starve the rest of the per-epoch budget.
	PendingReleaseCursorKey = collections.NewPrefix(31)
	// OrphanRecordsKey indexes the orphan unbond records (requested zero:
	// no payout is queued against them; epoch.go), so the epoch end finds a
	// validator's orphans without walking all its records (which grow with
	// every matured record whose payouts are not all made).
	OrphanRecordsKey = collections.NewPrefix(32)
	// The stake nullifier indexed tree (nf_tree.go): values by leaf index
	// (insertion order), its nodes and size, and the root and size recorded
	// at the end of the last block that changed it (what a snapshot takes).
	StakeNfValuesKey     = collections.NewPrefix(33)
	StakeNfNodesKey      = collections.NewPrefix(34)
	StakeNfSizeKey       = collections.NewPrefix(35)
	StakeNfLatestRootKey = collections.NewPrefix(36)
	StakeNfLatestSizeKey = collections.NewPrefix(37)
	// RootsStaleKey: set when recording the stake roots at a block's end
	// failed, cleared by the next success (audit 6 C-L4).
	RootsStaleKey = collections.NewPrefix(38)
	// Undelegation payouts (payouts.go): by id; the not-yet-tried ones by
	// (validator, epoch, id); the failed ones by (retry_at, id); the
	// MATURED records with payouts left; the id sequence.
	UnbondPayoutsKey   = collections.NewPrefix(39)
	PayoutsByRecordKey = collections.NewPrefix(40)
	PayoutRetriesKey   = collections.NewPrefix(41)
	MaturedRecordsKey  = collections.NewPrefix(42)
	UnbondPayoutSeqKey = collections.NewPrefix(43)
	// UsedVoteNullifiersKey holds every (proposal, vote nullifier) a note
	// vote used (votes.go): a vote's every note, not only its first, which
	// keys the vote.
	UsedVoteNullifiersKey = collections.NewPrefix(44)
	// ShelteredUnbondingsKey holds, by destination validator, the module's
	// x/staking unbonding delegation set aside while a slash of a validator
	// it redelegated from runs (redelegate.go): only inside BeginBlock,
	// emptied by this module's BeginBlocker; never exported.
	ShelteredUnbondingsKey = collections.NewPrefix(45)
	// Slash debt of private redelegations (moves.go, debt_tree.go):
	// MovesKey holds every move a slash of its source can still reach, by
	// move key; MovesByEntryKey indexes them by their x/staking entry
	// (src "/" dst, entry height, key); MovesByCompletionKey by when the
	// entry matures (completion ns, key).
	MovesKey             = collections.NewPrefix(46)
	MovesByEntryKey      = collections.NewPrefix(47)
	MovesByCompletionKey = collections.NewPrefix(48)
	// The slash debt indexed tree: its nodes, key -> leaf index (ordered by
	// key: the low-leaf lookup), leaf index -> key (insertion order), key ->
	// retained, and its leaf count.
	DebtNodesKey    = collections.NewPrefix(49)
	DebtIndexKey    = collections.NewPrefix(50)
	DebtLeafKeysKey = collections.NewPrefix(51)
	DebtRetainedKey = collections.NewPrefix(52)
	DebtSizeKey     = collections.NewPrefix(53)
	// MaxUnbondingKey is the longest x/staking unbonding_time seen (seconds):
	// the label window is this plus MoveTimeSlackSeconds.
	MaxUnbondingKey = collections.NewPrefix(54)
	// The slash in progress (moves.go): its source, and per destination the
	// module's shares there when it began and how many of the module's
	// entries x/staking unbonded there. Only inside BeginBlock.
	WatchSrcKey    = collections.NewPrefix(55)
	WatchSharesKey = collections.NewPrefix(56)
	WatchCallsKey  = collections.NewPrefix(57)
	// GwLapsesKey orders the Groundworks votes' leases by when they lapse:
	// (split_expires_at, vote id).
	GwLapsesKey = collections.NewPrefix(58)
	// GwVotesByTagKey maps a Groundworks vote's note tag to its id: what a
	// stake proof's input tags cancel.
	GwVotesByTagKey = collections.NewPrefix(59)
	// GwMaturesKey orders the Groundworks votes holding a pending exposure
	// by when it is due to count: (matures_at, vote id).
	GwMaturesKey = collections.NewPrefix(60)
)

// MoveTimeSlackSeconds is how far a redelegation's block time may be after
// the move_time its msg names (and so its label carries): the label window
// covers the x/staking entry's maturity from any block time in range.
const MoveTimeSlackSeconds = 600

// ClearBeforeSlackSeconds is how far below the label window's current
// clear_before (keeper ClearBefore) a stake proof's clear_before may be:
// every stake proof names it (audit 7, B L-1), made against a block at most
// this long before the one that includes it.
const ClearBeforeSlackSeconds = 3600

// MaxEntryHeightsPerPair bounds the module's x/staking redelegation entries
// of positive creation height per (src, dst): one per block with a bonded
// move (moves in one block share it). A move that would make one more first
// merges the two oldest into one (keeper mergeOldEntries): the later height,
// so a slash charges each of their moves at least as x/staking would charge
// its own, and the earlier completion, so no slash reaches a move whose label
// may have cleared. A move is never in an entry older than itself, so a
// slash for an infraction before it always reaches it (with no pair of
// entries small enough to merge, the move joins the latest entry instead,
// as a last resort, and an infraction between that entry's height and the
// move's falls on the source's stake). Each bonded move rewrites the pair's whole record, and pays gas for it
// (gasPerRedelegationEntry). Entries at height 0 or below (a zero-height
// export's) are never slashed again, are not counted and never merge.
const MaxEntryHeightsPerPair = 1024

// MaxMergeMoves bounds the moves one merge of two entries re-files (their
// entry height and completion change); a pair of entries holding more is
// passed over for the next (MergeTries pairs, oldest first).
const (
	MaxMergeMoves = 128
	MergeTries    = 8
)

// UnbondPayoutRetryDelay is how long after its attempts-th failure a payout
// is retried.
func UnbondPayoutRetryDelay(attempts uint32) int64 {
	shift := uint32(0)
	if attempts > 1 {
		shift = attempts - 1
	}
	if shift > UnbondPayoutRetryMaxShift {
		shift = UnbondPayoutRetryMaxShift
	}
	return int64(UnbondPayoutRetryBaseSeconds) << shift
}

// DerthDenom is validator's delegation token.
func DerthDenom(valoper string) string { return DerthPrefix + valoper }

// ParseDerthDenom returns the validator of a derth denom.
func ParseDerthDenom(denom string) (string, bool) {
	v, ok := strings.CutPrefix(denom, DerthPrefix)
	return v, ok && v != "" && !strings.Contains(v, "/")
}

// ValidatorVoterKey is the Groundworks voter key under which x/allocation
// weighs all of validator's votes together: "gwpos/" (kept from the retired
// positions: x/allocation stores voters under it) || the validator's
// address bytes (26 or 38 bytes, never an account's 20 or 32).
func ValidatorVoterKey(valBz []byte) []byte {
	return append([]byte(ValidatorVoterPrefix), valBz...)
}

// IsValidatorVoterKey reports whether key is a ValidatorVoterKey.
func IsValidatorVoterKey(key []byte) bool {
	n := len(key) - len(ValidatorVoterPrefix)
	return (n == 20 || n == 32) && string(key[:len(ValidatorVoterPrefix)]) == ValidatorVoterPrefix
}
