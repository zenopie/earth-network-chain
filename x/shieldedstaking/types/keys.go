// Package types defines private staking: the module as the sole non-self
// delegator, derth/<valoper> and unbond/<valoper>/<epoch> owner-locked stake
// notes in the module's own stake note tree, epochs, stake votes and
// Groundworks positions.
package types

import (
	"fmt"
	"strconv"
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

	// DerthPrefix and UnbondPrefix start this module's stake denoms. They name
	// stake note assets (zk/privacy.AssetID of the denom) and are never coins,
	// shielded-pool assets or dex tokens.
	DerthPrefix  = "derth/"
	UnbondPrefix = "unbond/"

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
	// books, unbond records and positions than this it is skipped (and an event says so)
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

	// ValidatorVoterPrefix starts a validator's Groundworks voter key in
	// x/allocation (ValidatorVoterKey).
	ValidatorVoterPrefix = "gwpos/"
)

// Storage prefixes.
var (
	ParamsKey         = collections.NewPrefix(0)
	EpochKey          = collections.NewPrefix(1)
	ValidatorsKey     = collections.NewPrefix(2)
	UnbondRecordsKey  = collections.NewPrefix(3)
	MaturityQueueKey  = collections.NewPrefix(4)
	PositionsKey      = collections.NewPrefix(5)
	PositionSeqKey    = collections.NewPrefix(6)
	SnapshotsKey      = collections.NewPrefix(7)
	VotesKey          = collections.NewPrefix(8)
	TalliesKey        = collections.NewPrefix(9)
	SnapshotExpiryKey = collections.NewPrefix(10)
	PositionsByValKey = collections.NewPrefix(11)
	PendingRecordsKey = collections.NewPrefix(12)
	// SlashedValidatorsKey holds the validators slashed in the current block,
	// whose epoch rate and positions EndBlock re-weighs. Emptied every
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
	// 26 was PositionCountKey (positions are no longer counted or capped).
	// RetiringEscrowsKey schedules (time, validator) the release of a reward
	// escrow whose operator removed its whole self-bond (escrow.go);
	// PendingReleasesKey holds removed validators whose release failed.
	RetiringEscrowsKey = collections.NewPrefix(27)
	PendingReleasesKey = collections.NewPrefix(28)
	// Groundworks totals (positions.go): per (validator, option) the sum of
	// derth x percent over the validator's live positions, and per validator
	// the Groundworks allocation epoch those totals belong to.
	GwTotalsKey = collections.NewPrefix(29)
	GwEpochKey  = collections.NewPrefix(30)
	// PendingReleaseCursorKey is the last PendingReleases entry the epoch
	// end retried: retries rotate through the set, so entries that keep
	// failing cannot starve the rest of the per-epoch budget.
	PendingReleaseCursorKey = collections.NewPrefix(31)
	// OrphanRecordsKey indexes the orphan unbond records (requested zero:
	// no note claims them; epoch.go), so the epoch end finds a validator's
	// orphans without walking all its records (which grow with every
	// unclaimed matured record).
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
)

// DerthDenom is validator's delegation token.
func DerthDenom(valoper string) string { return DerthPrefix + valoper }

// UnbondDenom is validator's unbonding claim for epoch.
func UnbondDenom(valoper string, epoch uint64) string {
	return fmt.Sprintf("%s%s/%d", UnbondPrefix, valoper, epoch)
}

// ParseDerthDenom returns the validator of a derth denom.
func ParseDerthDenom(denom string) (string, bool) {
	v, ok := strings.CutPrefix(denom, DerthPrefix)
	return v, ok && v != "" && !strings.Contains(v, "/")
}

// ParseUnbondDenom returns the validator and epoch of an unbond denom.
func ParseUnbondDenom(denom string) (string, uint64, bool) {
	rest, ok := strings.CutPrefix(denom, UnbondPrefix)
	if !ok {
		return "", 0, false
	}
	i := strings.LastIndexByte(rest, '/')
	if i <= 0 {
		return "", 0, false
	}
	e, err := strconv.ParseUint(rest[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return rest[:i], e, true
}

// ValidatorVoterKey is the Groundworks voter key under which x/allocation
// weighs all of validator's positions together: "gwpos/" || the validator's
// address bytes (26 or 38 bytes, never an account's 20 or 32).
func ValidatorVoterKey(valBz []byte) []byte {
	return append([]byte(ValidatorVoterPrefix), valBz...)
}

// IsValidatorVoterKey reports whether key is a ValidatorVoterKey.
func IsValidatorVoterKey(key []byte) bool {
	n := len(key) - len(ValidatorVoterPrefix)
	return (n == 20 || n == 32) && string(key[:len(ValidatorVoterPrefix)]) == ValidatorVoterPrefix
}
