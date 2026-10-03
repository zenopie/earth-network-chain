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

	// RemovalCooldown is how long after a removal ballot on an option closes,
	// in seconds, before another may be opened on it: thirty days.
	//
	// Opening a ballot is cheap (one membership proof a day per person) and
	// any one human can do it, so without a pause an option the chamber has
	// just declined to remove could be kept under a permanent rolling vote,
	// its payees never knowing from one week to the next whether they would
	// be paid. A cooldown lets the chamber's answer stand for a while. It is
	// a constant, not a param, for the reason every rule here is.
	RemovalCooldown = 30 * 24 * 60 * 60
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
	// BallotSeqKey hands out ballot ids. Every round of voting is its own
	// ballot: a proposal's round, the longer round it gets after the chamber
	// declines it on the expedited track, and each removal ballot. Votes are
	// filed under the ballot, so a round that has closed can never be read as
	// part of the next one, and closing a round is O(1) — its votes are left to
	// be cleared in capped batches (ClosedBallotsKey) instead of in the block it
	// closes in.
	BallotSeqKey = collections.NewPrefix("ballot_seq")

	// BallotVotesKey holds human votes on every ballot of both kinds.
	//
	// Keyed by the voter's membership nullifier for the ballot's scope: the
	// same person always presents the same one, so a second vote replaces the
	// first, and it links to nothing outside the ballot.
	BallotVotesKey = collections.NewPrefix("ballot_votes") // (ballot id, nullifier) -> VoteOption
	// BallotTallyKey is each OPEN ballot's running count, kept true as votes
	// arrive and as voters retire, so a ballot is decided without walking its
	// votes. A ballot with an entry here is open; closing it removes the entry.
	BallotTallyKey = collections.NewPrefix("ballot_tally") // ballot id -> Tally

	// ProposalBallotKey is the open ballot on each x/gov proposal in voting.
	ProposalBallotKey = collections.NewPrefix("proposal_ballot") // proposal id -> ballot id

	RemovalBallotsKey = collections.NewPrefix("removal_ballots") // option id -> RemovalBallot
	// RemovalBallotIDKey is the ballot behind each open removal record.
	RemovalBallotIDKey = collections.NewPrefix("removal_ballot_id") // option id -> ballot id
	// RemovalQueueKey orders open ballots by when they close, so the EndBlocker
	// can stop at the first one that is not due rather than walking them all.
	RemovalQueueKey = collections.NewPrefix("removal_queue") // (closes_at unix, option id)

	// ClosedBallotsKey is the ballots whose votes are still to be cleared.
	ClosedBallotsKey = collections.NewPrefix("closed_ballots") // ballot id

	// ProposalRoundKey is each proposal's current round of human voting when
	// it is past the first (after an expedited demotion).
	ProposalRoundKey = collections.NewPrefix("proposal_round") // proposal id -> ProposalRound

	// SubjectsKey is each proposal's subjects, fixed as it enters voting.
	SubjectsKey = collections.NewPrefix("subjects") // proposal id -> ProposalSubjects

	// RemovalCooldownKey is, per option, the earliest time a new removal
	// ballot on it may open (RemovalCooldown after its last one closed).
	RemovalCooldownKey = collections.NewPrefix("removal_cooldown") // option id -> unix seconds
)

// ClosedVotePurgeLimit caps how many votes of closed ballots one block clears.
// The rest wait for the next block; a closed ballot's votes are no longer read
// by anything, so the only cost of a backlog is the space it holds.
const ClosedVotePurgeLimit = 1000

// OrphanBallotCheckLimit caps how many open proposal ballots one block checks
// for a proposal that no longer exists. Every open ballot belongs to a
// proposal in its voting period, each of which cost a deposit to get there, so
// the set is small; the cap only keeps a pathological one from costing a block
// more than a bounded number of reads.
const OrphanBallotCheckLimit = 100
