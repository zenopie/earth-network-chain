package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/assembly sentinel errors
var (
	// ErrNotRegistered means the signer holds no live proof-of-personhood
	// registration. Weight in this chamber is a person, so there is nothing to
	// scale down to: an unregistered account does not vote at all.
	ErrNotRegistered = errors.Register(ModuleName, 1100, "signer holds no live personhood registration")

	// ErrProposalNotVoting means the named x/gov proposal does not exist or is
	// not in its voting period.
	ErrProposalNotVoting = errors.Register(ModuleName, 1101, "proposal is not in its voting period")

	// ErrBadVoteOption means the message named no side.
	ErrBadVoteOption = errors.Register(ModuleName, 1102, "vote must be yes or no")

	// ErrBallotExists means a removal ballot is already open against that option.
	ErrBallotExists = errors.Register(ModuleName, 1103, "a removal ballot is already open for this option")

	// ErrBallotNotFound means no removal ballot is open against that option.
	ErrBallotNotFound = errors.Register(ModuleName, 1104, "no open removal ballot for this option")

	// ErrNotRemovable means the option cannot be the subject of a removal ballot:
	// it does not exist, it is not on the groundworks stream, or it has already
	// been removed.
	ErrNotRemovable = errors.Register(ModuleName, 1105, "option is not removable by the assembly")
)
