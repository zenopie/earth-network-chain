// Package types defines private staking: the module as the sole non-self
// delegator, derth/<valoper> and unbond/<valoper>/<epoch> owner-locked stake
// notes in the module's own stake note tree, epochs, stake votes and
// Groundworks positions.
package types

import (
	"fmt"
	"strconv"
	"strings"

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
	// books or unbond records than this it is skipped (and an event says so)
	// rather than walk them all in EndBlock. Tests and genesis run it in full.
	InvariantBookLimit = 1_000

	// CheckpointPruneLimit caps how many dead supply checkpoints one block
	// deletes.
	CheckpointPruneLimit = 500

	// EscrowRetireLimit caps how many retired operators' escrows one block
	// releases.
	EscrowRetireLimit = 50

	// SnapshotSweepLimit caps how many finished proposals one block forgets.
	SnapshotSweepLimit = 20

	// PositionKeyPrefix starts a position's voter key in x/allocation (17
	// bytes: cannot collide with a 20- or 32-byte account address).
	PositionKeyPrefix = "position:"
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
	StakeTreeNodesKey   = collections.NewPrefix(14)
	StakeTreeSizeKey    = collections.NewPrefix(15)
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
	// PositionCountKey counts Positions.
	PositionCountKey = collections.NewPrefix(26)
	// RetiringEscrowsKey schedules (time, validator) the release of a reward
	// escrow whose operator removed its whole self-bond (escrow.go);
	// PendingReleasesKey holds removed validators whose release failed.
	RetiringEscrowsKey = collections.NewPrefix(27)
	PendingReleasesKey = collections.NewPrefix(28)
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

// PositionVoterKey is position id's voter key in x/allocation.
func PositionVoterKey(id uint64) []byte {
	b := []byte(PositionKeyPrefix)
	return append(b, byte(id>>56), byte(id>>48), byte(id>>40), byte(id>>32), byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
}

// ParsePositionVoterKey is PositionVoterKey's inverse.
func ParsePositionVoterKey(key []byte) (uint64, bool) {
	if len(key) != len(PositionKeyPrefix)+8 || string(key[:len(PositionKeyPrefix)]) != PositionKeyPrefix {
		return 0, false
	}
	var id uint64
	for _, b := range key[len(PositionKeyPrefix):] {
		id = id<<8 | uint64(b)
	}
	return id, true
}
