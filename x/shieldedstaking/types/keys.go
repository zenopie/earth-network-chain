// Package types defines private staking: the shielded module as the sole
// non-self delegator, derth/<valoper> and unbond/<valoper>/<epoch> notes,
// epochs, stake votes and Groundworks positions.
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

	// DerthPrefix and UnbondPrefix start this module's shielded-only denoms.
	DerthPrefix  = "derth/"
	UnbondPrefix = "unbond/"

	// MaxOptionsPerVote bounds a stake vote's weighted options (x/gov has four).
	MaxOptionsPerVote = 4

	// EpochValidatorLimit caps how many validators one epoch end processes.
	// A validator the cap leaves out keeps its queue until the next epoch.
	EpochValidatorLimit = 200

	// MaturityLimit caps how many unbonding records one block matures. The
	// rest wait a block: their SDK entries also wait for x/staking, which pays
	// every mature entry each block, so a record that misses its block is
	// read at the next block with its entry already gone — see matureRecords.
	MaturityLimit = 500

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
