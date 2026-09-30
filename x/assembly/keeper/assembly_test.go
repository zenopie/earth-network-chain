package keeper

import (
	"cosmossdk.io/collections"
	"errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"testing"
	"time"

	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// TestApproves pins the rule the whole chamber turns on.
//
// Two thirds of the votes cast, no quorum, no floor. The two ends are the ones
// worth writing down, because both are consequences people find surprising and
// both were chosen deliberately: silence fails a proposal, and one voter alone
// can carry one.
func TestApproves(t *testing.T) {
	require.False(t, types.Approves(0, 0), "silence must not pass a proposal")
	require.True(t, types.Approves(1, 0), "one voter is one of one, which is over two thirds")
	require.True(t, types.Approves(2, 1), "exactly two thirds passes")
	require.False(t, types.Approves(1, 1), "a half is not two thirds")
	require.False(t, types.Approves(6, 4), "three fifths is not two thirds")
	require.True(t, types.Approves(667, 333))
	require.False(t, types.Approves(666, 334))
}

// A proposal the chamber did not ratify is failed before x/gov can tally it,
// and x/gov's own verdict on the stake vote is recorded rather than lost.
func TestUnratifiedProposalIsFailedBeforeGovTallies(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)

	// Nobody votes. This is the case the design treats as a refusal.
	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusFailed, proposal.Status)
	require.Contains(t, proposal.FailedReason, "assembly")
	require.NotNil(t, proposal.FinalTallyResult,
		"the stake tally is recorded even when the chamber is what ended it — "+
			"a proposal that vanished with no tally would hide what stake wanted")

	// Gone from both of x/gov's live indexes, so its own EndBlocker cannot see it.
	has, err := e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(end, 1))
	require.NoError(t, err)
	require.False(t, has, "a refused proposal must leave the active queue")
	voting, err := e.gov.VotingPeriodProposals.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, voting)
}

// TestRatifiedProposalIsLeftForGov is the guard on the subtlest failure this
// design has.
//
// x/gov's tally deletes the votes it counts. If the chamber tallied a proposal
// it had approved — for a record, for an event, for anything — x/gov would then
// count an empty set a moment later and fail a proposal both houses passed. So
// an approved proposal must come out of this EndBlocker untouched, and that is
// what is checked here rather than merely intended.
func TestRatifiedProposalIsLeftForGov(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)

	_, alice := e.addr(t, "alice", "null-alice")
	_, bob := e.addr(t, "bob", "null-bob")
	for _, v := range []string{alice, bob} {
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{
			Membership: voter(v), ProposalId: 1, Option: types.VOTE_OPTION_YES,
		})
		require.NoError(t, err)
	}

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, proposal.Status,
		"an approved proposal is x/gov's to resolve, and it has not run yet")
	has, err := e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(end, 1))
	require.NoError(t, err)
	require.True(t, has, "an approved proposal must still be in the queue x/gov reads")
}

// Votes only go to a proposal that is open for them.
func TestVotingNeedsAnOpenProposal(t *testing.T) {
	e := newTestEnv(t)
	_, alice := e.addr(t, "alice", "null-alice")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{
		Membership: voter(alice), ProposalId: 42, Option: types.VOTE_OPTION_YES,
	})
	require.ErrorIs(t, err, types.ErrProposalNotVoting)
}

// A removal ballot that carries strikes the option; one that does not, does not.
func TestRemovalBallot(t *testing.T) {
	e := newTestEnv(t)
	e.allocation.removable[7] = true

	_, alice := e.addr(t, "alice", "null-alice")
	_, bob := e.addr(t, "bob", "null-bob")
	_, carol := e.addr(t, "carol", "null-carol")

	opened, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(alice), OptionId: 7})
	require.NoError(t, err)

	// One ballot per option: a second would let anyone keep an option under a
	// permanent rolling vote.
	_, err = e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(bob), OptionId: 7})
	require.ErrorIs(t, err, types.ErrBallotExists)

	for _, v := range []string{alice, bob} {
		_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{
			Membership: voter(v), OptionId: 7, Option: types.VOTE_OPTION_YES,
		})
		require.NoError(t, err)
	}
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{
		Membership: voter(carol), OptionId: 7, Option: types.VOTE_OPTION_NO,
	})
	require.NoError(t, err)

	// Two of three is exactly two thirds, so it carries.
	e.ctx = e.ctx.WithBlockTime(time.Unix(opened.ClosesAt+1, 0))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.Equal(t, []uint64{7}, e.allocation.removed)

	// The ballot and its votes are gone with it.
	has, err := e.k.RemovalBallots.Has(e.ctx, 7)
	require.NoError(t, err)
	require.False(t, has)
}

// A ballot that falls short leaves the option alone.
func TestRemovalBallotThatFallsShort(t *testing.T) {
	e := newTestEnv(t)
	e.allocation.removable[7] = true

	_, alice := e.addr(t, "alice", "null-alice")
	_, bob := e.addr(t, "bob", "null-bob")

	opened, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(alice), OptionId: 7})
	require.NoError(t, err)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Membership: voter(alice), OptionId: 7, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Membership: voter(bob), OptionId: 7, Option: types.VOTE_OPTION_NO})
	require.NoError(t, err)

	e.ctx = e.ctx.WithBlockTime(time.Unix(opened.ClosesAt+1, 0))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.Empty(t, e.allocation.removed, "one of two is not two thirds")
}

// Only an option x/allocation says is removable can be balloted against.
func TestRemovalNeedsARemovableOption(t *testing.T) {
	e := newTestEnv(t)
	_, alice := e.addr(t, "alice", "null-alice")
	_, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(alice), OptionId: 7})
	require.ErrorIs(t, err, types.ErrNotRemovable)
}

// Genesis carries votes in flight, and rebuilds the tallies from them.
func TestGenesisRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)
	e.allocation.removable[7] = true

	_, alice := e.addr(t, "alice", "null-alice")
	_, bob := e.addr(t, "bob", "null-bob")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(alice), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	_, err = e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(bob), ProposalId: 1, Option: types.VOTE_OPTION_NO})
	require.NoError(t, err)
	_, err = e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(alice), OptionId: 7})
	require.NoError(t, err)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Membership: voter(alice), OptionId: 7, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())

	fresh := newTestEnv(t)
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))

	tally, err := fresh.k.proposalTally(fresh.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), tally.Yes)
	require.Equal(t, uint64(1), tally.No)

	rtally, err := fresh.k.removalTally(fresh.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, uint64(1), rtally.Yes,
		"the tally is rebuilt from the votes, not carried alongside them")

	again, err := fresh.k.ExportGenesis(fresh.ctx)
	require.NoError(t, err)
	require.Equal(t, exported.ProposalVotes, again.ProposalVotes)
	require.Equal(t, exported.RemovalBallots, again.RemovalBallots)
}

// The expedited track asks the chamber for three quarters, not two thirds.
//
// The fast track trades seven days of deliberation for one. It pays for that in
// agreement — which is the reason to have one at all — so the chamber asks more
// there, matching the 0.75 the stake house asks of the same proposal.
func TestExpeditedNeedsThreeQuarters(t *testing.T) {
	require.True(t, types.Approves(2, 1), "two thirds carries an ordinary proposal")
	require.False(t, types.ApprovesExpedited(2, 1), "two thirds does not carry an expedited one")
	require.True(t, types.ApprovesExpedited(3, 1), "exactly three quarters does")
	require.False(t, types.ApprovesExpedited(0, 0), "silence still refuses")
}

// An expedited proposal the chamber declines is demoted to a regular one rather
// than killed, the same way x/gov handles its own expedited tally falling short.
//
// Declining on the fast track can mean "not in one day" as easily as "never", and
// the longer round costs the people who refused it nothing: silence fails that
// round too.
func TestDeclinedExpeditedProposalIsDemotedNotKilled(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(24 * time.Hour)
	e.openExpedited(t, 1, end)

	// Two of three: enough for an ordinary proposal, short of three quarters.
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "alice", "bob")
	e.voteAll(t, 1, types.VOTE_OPTION_NO, "carol")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, proposal.Expedited, "it comes off the fast track")
	require.Equal(t, v1.StatusVotingPeriod, proposal.Status, "and is still alive to be voted on")

	params, err := e.gov.Params.Get(e.ctx)
	require.NoError(t, err)
	require.Equal(t, proposal.VotingStartTime.Add(*params.VotingPeriod), *proposal.VotingEndTime,
		"with a full regular voting period ahead of it")
	require.True(t, proposal.VotingEndTime.After(e.ctx.BlockTime()),
		"the new end must be in the future, or x/gov tallies it unratified in this same block")

	// Re-queued at the new time, and gone from the old key so x/gov's own
	// iteration over what is due now does not pick it up.
	has, err := e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(end, 1))
	require.NoError(t, err)
	require.False(t, has)
	has, err = e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(*proposal.VotingEndTime, 1))
	require.NoError(t, err)
	require.True(t, has)

	// The one-day tally does not carry into the longer round: it was taken under
	// a different bar over a different window, and is counted again from zero.
	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{}, tally)

	// The regular round is a new ballot scope, opened now: a voter proves an
	// identity activated a root window before the demotion, and alice's
	// nullifier in it is a new one.
	in, err := NewQueryServerImpl(e.k).BallotInputs(e.ctx, &types.QueryBallotInputsRequest{ProposalId: 1})
	require.NoError(t, err)
	require.Equal(t, uint64(1), in.Round)
	require.Equal(t, privacy.FieldBytes(privacy.ProposalScope(1, 1)), in.Scope)
	require.Equal(t, uint64(e.ctx.BlockTime().Unix()-3600), in.MaxActivation)

	// And the second round runs under ordinary rules: two thirds now suffices.
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "dave", "erin")
	e.voteAll(t, 1, types.VOTE_OPTION_NO, "frank")
	e.ctx = e.ctx.WithBlockTime(proposal.VotingEndTime.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	after, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, after.Status, "ratified, and left for x/gov to resolve")
}

// An expedited proposal that clears three quarters is left alone, exactly like
// any other ratified proposal: x/gov still owes it its own expedited tally.
func TestRatifiedExpeditedProposalIsLeftForGov(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(24 * time.Hour)
	e.openExpedited(t, 1, end)
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "alice", "bob", "carol")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, proposal.Expedited, "it stays on the fast track")
	require.Equal(t, v1.StatusVotingPeriod, proposal.Status)
	has, err := e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(end, 1))
	require.NoError(t, err)
	require.True(t, has)
}

// The demotion has one guard, and this is it.
//
// x/gov's EndBlocker runs a moment after the chamber's, over the same "due now"
// range. A proposal re-queued into the past would therefore be tallied by the
// stake house in this very block — with the chamber's refusal already spent and
// no second round left in which to express it. So when the recomputed end time
// is not actually in the future, the proposal is refused outright instead.
func TestDemotionThatCannotExtendTheVoteRefusesInstead(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(24 * time.Hour)
	e.openExpedited(t, 1, end)
	e.voteAll(t, 1, types.VOTE_OPTION_NO, "alice")

	// A regular voting period shorter than the expedited one already elapsed:
	// a misconfiguration, but one with teeth.
	params, err := e.gov.Params.Get(e.ctx)
	require.NoError(t, err)
	second := time.Second
	params.VotingPeriod = &second
	require.NoError(t, e.gov.Params.Set(e.ctx, params))

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusFailed, proposal.Status,
		"a demotion that cannot extend the vote must refuse, never leave it in the queue")
	require.Contains(t, proposal.FailedReason, "three quarters",
		"the record names the bar it actually had to clear")
	has, err := e.gov.ActiveProposalsQueue.Has(e.ctx, collKeyTime(end, 1))
	require.NoError(t, err)
	require.False(t, has)
}

// TestRefusedProposalKeepsStakesDepositJudgement: the chamber refunded every
// deposit it failed, so a proposal stake would have burned (spam, a veto) got
// its deposit back whenever humans also said no.
func TestRefusedProposalKeepsStakesDepositJudgement(t *testing.T) {
	for _, burnOnQuorum := range []bool{true, false} {
		e := newTestEnv(t)
		params, err := e.gov.Params.Get(e.ctx)
		require.NoError(t, err)
		params.BurnVoteQuorum = burnOnQuorum
		require.NoError(t, e.gov.Params.Set(e.ctx, params))

		end := e.ctx.BlockTime().Add(time.Hour)
		e.openProposal(t, 1, end)
		depositor, _ := e.addr(t, "depositor", "")
		deposit := sdk.NewCoins(sdk.NewInt64Coin("uerth", 500))
		require.NoError(t, e.gov.SetDeposit(e.ctx, v1.NewDeposit(1, depositor, deposit)))

		// Nobody votes in either house: stake misses quorum, humans refuse.
		e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
		require.NoError(t, e.k.EndBlocker(e.ctx))

		if burnOnQuorum {
			require.Equal(t, deposit, *e.govBank.burned, "stake would have burned this deposit")
		} else {
			require.True(t, e.govBank.burned.IsZero(), "stake would have refunded this deposit")
		}
		has, err := e.gov.Deposits.Has(e.ctx, collections.Join(uint64(1), depositor))
		require.NoError(t, err)
		require.False(t, has, "the deposit is settled one way or the other")
	}
}

// TestCancelledProposalsBallotCloses: x/gov deletes a proposal cancelled while
// voting, with no hook, and its ballot used to stay open forever.
func TestCancelledProposalsBallotCloses(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "alice")
	ballot, ok, err := e.k.proposalBallot(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, e.gov.DeleteProposal(e.ctx, 1))
	require.NoError(t, e.k.EndBlocker(e.ctx))

	_, ok, err = e.k.proposalBallot(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, ok, "the cancelled proposal's ballot is closed")
	has, err := e.k.BallotTally.Has(e.ctx, ballot)
	require.NoError(t, err)
	require.False(t, has)
}

// TestFailedStrikeDoesNotHaltTheChain: the strike runs in EndBlock, where an
// error used to halt every validator at once.
func TestFailedStrikeDoesNotHaltTheChain(t *testing.T) {
	e := newTestEnv(t)
	e.allocation.removable[7] = true
	e.allocation.failWith = errors.New("allocation refused")

	_, alice := e.addr(t, "alice", "null-alice")
	opened, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Membership: voter(alice), OptionId: 7})
	require.NoError(t, err)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Membership: voter(alice), OptionId: 7, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)

	e.ctx = e.ctx.WithBlockTime(time.Unix(opened.ClosesAt+1, 0))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.Empty(t, e.allocation.removed)

	has, err := e.k.RemovalBallots.Has(e.ctx, 7)
	require.NoError(t, err)
	require.False(t, has, "the ballot still closes")
}

// The same nullifier voting again replaces its vote rather than adding one.
func TestSameNullifierReplacesItsVote(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	_, a := e.addr(t, "alice", "n")
	for _, opt := range []types.VoteOption{types.VOTE_OPTION_YES, types.VOTE_OPTION_NO, types.VOTE_OPTION_NO} {
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(a), ProposalId: 1, Option: opt})
		require.NoError(t, err)
	}
	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{No: 1}, tally)
}

// A msg reaching the handler without the private ante is refused.
func TestHandlerRefusesWithoutTheAnte(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	_, err := NewMsgServerImpl(e.k).VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter("x"), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.ErrorIs(t, err, shieldedtypes.ErrUnauthorized)
}
