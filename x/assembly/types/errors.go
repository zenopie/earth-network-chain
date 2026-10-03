package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/assembly sentinel errors
var (
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

	// ErrTooManySubjects means the proposal's revocations cannot be excluded
	// from its vote by one membership proof, which excludes one Document
	// Signer and one country: they span countries, or one of unknown country
	// is among several. The chamber cannot keep every subject out of the vote,
	// so such a proposal takes no human votes (and so fails) and must be split
	// per country. See keeper/subjects.go.
	// ErrRemovalCooldown means a removal ballot on the option closed less than
	// RemovalCooldown ago.
	ErrRemovalCooldown = errors.Register(ModuleName, 1108, "a removal ballot on this option closed too recently")

	ErrTooManySubjects = errors.Register(ModuleName, 1107, "proposal's revocations span more than one country or signer; the chamber votes on one country at a time")
)
