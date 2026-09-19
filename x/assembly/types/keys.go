package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the module name.
	ModuleName = "assembly"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName
)

// The chamber's rules, as constants.
//
// There is no Params message and no MsgUpdateParams anywhere in this module, and
// that is the point rather than an omission. The assembly exists to check
// stake-weighted governance; if x/gov could set the assembly's threshold it
// could set the threshold to something unreachable and the check would be
// decorative. Moving any of these means shipping a binary every validator
// chooses to run — the same protection the emission split gets in
// x/earth/types/keys.go, for the same reason.
const (
	// ApprovalNum / ApprovalDen is the fraction of the votes cast that must be
	// YES for a ballot to carry: two thirds.
	//
	// Measured against votes cast, with no quorum and no minimum turnout. Two
	// consequences follow directly and are accepted deliberately. A proposal
	// nobody votes on fails, upgrades included — apathy freezes governance
	// rather than waving it through. And a single YES vote is one of one, which
	// clears the bar, so while the registry is small the chamber's decisions rest
	// on whoever shows up.
	ApprovalNum = 2
	ApprovalDen = 3

	// ExpeditedApprovalNum / ExpeditedApprovalDen is the bar for a proposal on
	// x/gov's expedited track: three quarters, matching what the stake house asks
	// of the same proposal (expedited_threshold is 0.75 in genesis).
	//
	// The fast track buys a one-day voting period instead of seven. It pays for
	// that in agreement rather than in deliberation, which is the whole logic of
	// having one — so the chamber asks for more there too, not less.
	ExpeditedApprovalNum = 3
	ExpeditedApprovalDen = 4

	// RemovalVotingPeriod is how long a removal ballot stays open, in seconds.
	//
	// Shorter than x/gov's voting period on purpose: a removal is a single
	// yes-or-no about one option that is already public and already being paid,
	// not a proposal anyone needs to go read.
	RemovalVotingPeriod = 7 * 24 * 60 * 60
)

// Approves reports whether a tally carries at the ordinary two-thirds bar. Used
// for regular proposals and for removal ballots.
func Approves(yes, no uint64) bool {
	return approves(yes, no, ApprovalNum, ApprovalDen)
}

// ApprovesExpedited reports whether a tally carries at the three-quarters bar
// the expedited track asks for.
func ApprovesExpedited(yes, no uint64) bool {
	return approves(yes, no, ExpeditedApprovalNum, ExpeditedApprovalDen)
}

// approves is the shared arithmetic.
//
// Integer on purpose: yes*den >= (yes+no)*num has no rounding question at the
// boundary, so exactly two thirds passes and nothing depends on how a decimal
// type happens to round. A ballot with no votes at all does not carry — 0 >= 0
// would say otherwise, so it is refused explicitly.
func approves(yes, no, num, den uint64) bool {
	if yes == 0 && no == 0 {
		return false
	}
	return yes*den >= (yes+no)*num
}

var (
	// ProposalVotesKey holds human votes on x/gov proposals.
	//
	// Keyed by the registration's nullifier rather than by the voter's address:
	// x/personhood lets a registration move to a new wallet, so an address key
	// would let one person vote, move, and vote again.
	ProposalVotesKey = collections.NewPrefix("proposal_votes") // (proposal id, nullifier) -> VoteOption
	// ProposalTallyKey is the running count, so resolving a proposal does not
	// have to walk its votes in the EndBlocker.
	ProposalTallyKey = collections.NewPrefix("proposal_tally") // proposal id -> Tally

	RemovalBallotsKey = collections.NewPrefix("removal_ballots") // option id -> RemovalBallot
	RemovalVotesKey   = collections.NewPrefix("removal_votes")   // (option id, nullifier) -> VoteOption
	// RemovalQueueKey orders open ballots by when they close, so the EndBlocker
	// can stop at the first one that is not due rather than walking them all.
	RemovalQueueKey = collections.NewPrefix("removal_queue") // (closes_at unix, option id)
)
