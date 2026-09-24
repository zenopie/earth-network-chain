package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
)

// retire is what x/personhood does when a registration stops counting: it
// leaves the roll, and the chamber is told.
func (e *testEnv) retire(t *testing.T, acc sdk.AccAddress, nullifier string) {
	t.Helper()
	e.humans.lapse(acc)
	require.NoError(t, e.k.OnRegistrationRetired(e.ctx, []byte(nullifier)))
}

// A registration that stops counting has its votes taken back, so the tally a
// ballot closes on is only ever live humans.
//
// The case this exists for is a revoked Document Signer: its registrations vote
// on other proposals, governance revokes the signer, and as the purge retires
// them their votes come off every open ballot.
func TestRetiredRegistrationsVotesAreTakenBack(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)

	_, honest := e.addr(t, "honest", "null-honest")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: honest, ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)

	var forged []sdk.AccAddress
	for _, name := range []string{"forged-a", "forged-b"} {
		acc, a := e.addr(t, name, "null-"+name)
		forged = append(forged, acc)
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: a, ProposalId: 1, Option: types.VOTE_OPTION_NO})
		require.NoError(t, err)
	}
	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1, No: 2}, tally)

	e.retire(t, forged[0], "null-forged-a")
	e.retire(t, forged[1], "null-forged-b")

	tally, err = e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1}, tally, "the retired votes must come off the running tally")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	proposal, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, proposal.Status,
		"with the retired votes gone the one live YES carries, so the proposal is left for x/gov")
}

// The same on a removal ballot.
func TestRetiredRegistrationsRemovalVotesAreTakenBack(t *testing.T) {
	e := newTestEnv(t)
	e.allocation.removable[7] = true

	_, alice := e.addr(t, "alice", "null-alice")
	bobAcc, bob := e.addr(t, "bob", "null-bob")
	carolAcc, carol := e.addr(t, "carol", "null-carol")

	opened, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Proposer: alice, OptionId: 7})
	require.NoError(t, err)
	for _, voter := range []string{bob, carol} {
		_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Voter: voter, OptionId: 7, Option: types.VOTE_OPTION_YES})
		require.NoError(t, err)
	}
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Voter: alice, OptionId: 7, Option: types.VOTE_OPTION_NO})
	require.NoError(t, err)

	// Two of three would carry. With both YES voters retired it is none of one.
	e.retire(t, bobAcc, "null-bob")
	e.retire(t, carolAcc, "null-carol")

	e.ctx = e.ctx.WithBlockTime(time.Unix(opened.ClosesAt+1, 0))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.Empty(t, e.allocation.removed)
}

// Closing a ballot does not touch its votes: they are cleared afterwards, a
// capped number per block, index entries with them. A voter retired while that
// is still under way costs nothing extra and does not fail.
func TestClosedBallotsAreClearedInCappedBatches(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	e.openProposal(t, 1, end)
	accs := map[string]sdk.AccAddress{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		acc, voter := e.addr(t, name, "null-"+name)
		accs[name] = acc
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: voter, ProposalId: 1, Option: types.VOTE_OPTION_YES})
		require.NoError(t, err)
	}
	ballot, ok, err := e.k.proposalBallot(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)

	// Close the round without the EndBlocker's own purge, to watch it step.
	require.NoError(t, e.k.endProposalRound(e.ctx, 1))
	require.Equal(t, 5, countVotes(t, e, ballot), "closing leaves the votes for later")

	require.NoError(t, e.k.purgeClosedBallots(e.ctx, 2))
	require.Equal(t, 3, countVotes(t, e, ballot), "one call clears at most its budget")

	e.retire(t, accs["e"], "null-e")

	require.NoError(t, e.k.purgeClosedBallots(e.ctx, 2))
	require.NoError(t, e.k.purgeClosedBallots(e.ctx, 2))
	require.Zero(t, countVotes(t, e, ballot))
	has, err := e.k.ClosedBallots.Has(e.ctx, ballot)
	require.NoError(t, err)
	require.False(t, has, "a cleared ballot leaves the queue")

	n := 0
	require.NoError(t, e.k.VotedBallots.Walk(e.ctx, nil, func(collections.Pair[[]byte, uint64]) (bool, error) {
		n++
		return false, nil
	}))
	require.Zero(t, n, "and leaves nothing in the index")
}

func countVotes(t *testing.T, e *testEnv, ballot uint64) int {
	t.Helper()
	n := 0
	rng := collections.NewPrefixedPairRange[uint64, []byte](ballot)
	require.NoError(t, e.k.BallotVotes.Walk(e.ctx, rng, func(collections.Pair[uint64, []byte], int32) (bool, error) {
		n++
		return false, nil
	}))
	return n
}

// A removal ballot reopened on the same option starts from nothing, even while
// the last one's votes are still waiting to be cleared.
func TestReopenedRemovalBallotStartsEmpty(t *testing.T) {
	e := newTestEnv(t)
	e.allocation.removable[7] = true
	_, alice := e.addr(t, "alice", "null-alice")

	opened, err := e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Proposer: alice, OptionId: 7})
	require.NoError(t, err)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Voter: alice, OptionId: 7, Option: types.VOTE_OPTION_NO})
	require.NoError(t, err)

	// Close it without clearing, as a block over its purge budget would.
	require.NoError(t, e.k.closeRemovalBallot(e.ctx, collections.Join(opened.ClosesAt, uint64(7)), 7))

	_, err = e.ms.ProposeRemoval(e.ctx, &types.MsgProposeRemoval{Proposer: alice, OptionId: 7})
	require.NoError(t, err)
	tally, err := e.k.removalTally(e.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, types.Tally{}, tally)
	_, err = e.ms.VoteRemoval(e.ctx, &types.MsgVoteRemoval{Voter: alice, OptionId: 7, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	tally, err = e.k.removalTally(e.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1}, tally, "a vote on the new ballot is a first vote, not a change of the old one")
}

// Votes in the v0.9.0 layout move onto ballots at the upgrade, and are then
// counted and retracted like any other.
func TestMigrateToBallotsMovesLegacyVotes(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	acc, _ := e.addr(t, "voter", "null-voter")
	_, _ = e.addr(t, "other", "null-other")
	require.NoError(t, e.k.LegacyProposalVotes.Set(e.ctx, collKey(1, []byte("null-voter")), int32(types.VOTE_OPTION_NO)))
	require.NoError(t, e.k.LegacyProposalVotes.Set(e.ctx, collKey(1, []byte("null-other")), int32(types.VOTE_OPTION_YES)))
	require.NoError(t, e.k.LegacyProposalTally.Set(e.ctx, 1, types.Tally{Yes: 1, No: 1}))

	e.allocation.removable[7] = true
	require.NoError(t, e.k.RemovalBallots.Set(e.ctx, 7, types.RemovalBallot{OptionId: 7, ClosesAt: 99}))
	require.NoError(t, e.k.LegacyRemovalVotes.Set(e.ctx, collKey(7, []byte("null-voter")), int32(types.VOTE_OPTION_YES)))

	require.NoError(t, e.k.MigrateToBallots(e.ctx))

	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1, No: 1}, tally)
	rtally, err := e.k.removalTally(e.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1}, rtally)

	for _, legacy := range []collections.Map[collections.Pair[uint64, []byte], int32]{e.k.LegacyProposalVotes, e.k.LegacyRemovalVotes} {
		n := 0
		require.NoError(t, legacy.Walk(e.ctx, nil, func(collections.Pair[uint64, []byte], int32) (bool, error) {
			n++
			return false, nil
		}))
		require.Zero(t, n, "the old layout is emptied")
	}

	e.retire(t, acc, "null-voter")
	tally, err = e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1}, tally)
	rtally, err = e.k.removalTally(e.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, types.Tally{}, rtally)
}
