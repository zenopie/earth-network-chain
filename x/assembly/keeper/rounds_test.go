package keeper

import (
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
)

// Audit 5 L-AS1: an expedited proposal the chamber ratifies and x/gov then
// demotes votes again in a new ballot scope, so a voter's nullifier does not
// repeat across the two rounds.
func TestGovDemotedRoundHasItsOwnScope(t *testing.T) {
	e := newTestEnv(t)
	e.gov.SetHooks(govtypes.NewMultiGovHooks(e.k.GovHooks()))
	end := e.ctx.BlockTime().Add(24 * time.Hour)
	p := e.openExpedited(t, 1, end)
	before, round, err := e.k.proposalInputs(e.ctx, p)
	require.NoError(t, err)
	require.Zero(t, round)
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "alice")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.NoError(t, gov.EndBlocker(e.ctx, e.gov)) // no stake quorum: demoted
	after, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, after.Status)
	require.False(t, after.Expedited)

	st, round, err := e.k.proposalInputs(e.ctx, after)
	require.NoError(t, err)
	require.Equal(t, uint64(1), round)
	require.NotEqual(t, before.Scope, st.Scope, "a new round, a new nullifier scope")
	// The same human votes again, in the new ballot.
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "alice")
	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), tally.Yes)

	// Once x/gov ends it, the round goes with it.
	e.ctx = e.ctx.WithBlockTime(after.VotingEndTime.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.NoError(t, gov.EndBlocker(e.ctx, e.gov))
	has, err := e.k.ProposalRound.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, has)
}

// The hook drops a next round x/gov ended instead of demoting.
func TestEndedProposalDropsItsRound(t *testing.T) {
	e := newTestEnv(t)
	p := e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	require.NoError(t, e.k.ProposalRound.Set(e.ctx, 1, types.ProposalRound{Round: 1, OpenedAt: 5}))
	p.Status = v1.StatusPassed
	require.NoError(t, e.gov.SetProposal(e.ctx, p))
	require.NoError(t, e.k.GovHooks().AfterProposalVotingPeriodEnded(e.ctx, 1))
	has, err := e.k.ProposalRound.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, has)
}

// Audit 5 L-AS3: ProposalTally applies the proposal's own bar.
func TestProposalTallyUsesTheExpeditedBar(t *testing.T) {
	e := newTestEnv(t)
	e.openExpedited(t, 1, e.ctx.BlockTime().Add(time.Hour))
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "a", "b")
	e.voteAll(t, 1, types.VOTE_OPTION_NO, "c")
	res, err := NewQueryServerImpl(e.k).ProposalTally(e.ctx, &types.QueryProposalTallyRequest{ProposalId: 1})
	require.NoError(t, err)
	require.False(t, res.Approved, "2/3 does not carry an expedited proposal")
	e.openProposal(t, 2, e.ctx.BlockTime().Add(time.Hour))
	e.voteAll(t, 2, types.VOTE_OPTION_YES, "a", "b")
	e.voteAll(t, 2, types.VOTE_OPTION_NO, "c")
	res, err = NewQueryServerImpl(e.k).ProposalTally(e.ctx, &types.QueryProposalTallyRequest{ProposalId: 2})
	require.NoError(t, err)
	require.True(t, res.Approved)
}

// Audit 5 L-AS4: a ballot of a proposal that left voting without this module
// closing it is closed by the sweep, which resumes past where it stopped.
func TestStaleBallotSwept(t *testing.T) {
	e := newTestEnv(t)
	for _, id := range []uint64{1, 2} {
		e.openProposal(t, id, e.ctx.BlockTime().Add(time.Hour))
		e.voteAll(t, id, types.VOTE_OPTION_YES, "v")
	}
	p, err := e.gov.Proposals.Get(e.ctx, 2)
	require.NoError(t, err)
	p.Status = v1.StatusRejected
	require.NoError(t, e.gov.SetProposal(e.ctx, p))

	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 1)) // looks at 1 only
	_, open, err := e.k.proposalBallot(e.ctx, 2)
	require.NoError(t, err)
	require.True(t, open)
	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 1)) // resumes at 2
	_, open, err = e.k.proposalBallot(e.ctx, 2)
	require.NoError(t, err)
	require.False(t, open, "closed")
	_, open, err = e.k.proposalBallot(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, open, "a live ballot stays")
}
