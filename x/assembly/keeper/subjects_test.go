package keeper

import (
	"context"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
)

type countingPki struct {
	*stubPki
	calls int
}

func (c *countingPki) DscIssuerCountry(ctx context.Context, der []byte) (string, bool, error) {
	c.calls++
	return c.stubPki.DscIssuerCountry(ctx, der)
}

func (c *countingPki) CscaKeyCountry(ctx context.Context, der []byte) (string, error) {
	c.calls++
	return c.stubPki.CscaKeyCountry(ctx, der)
}

// AUDIT3 M2 (TestPOC_GovDemotedExpeditedLosesFixedSubjects): an expedited
// proposal the chamber ratifies but stake does not is demoted by x/gov
// itself and stays in voting. Its subjects must stay the ones fixed when it
// entered voting: a trust-store change mid-vote does not move them, and no
// vote (nor junk vote in CheckTx) re-classifies it. They go once x/gov ends
// its voting for good.
func TestGovDemotedExpeditedKeepsFixedSubjects(t *testing.T) {
	e := newTestEnv(t)
	cp := &countingPki{stubPki: e.pki}
	e.k.pki = cp
	e.ms = privateServer{k: e.k, ms: NewMsgServerImpl(e.k)}
	e.gov.SetHooks(govtypes.NewMultiGovHooks(e.k.GovHooks()))

	revA, _ := revokeAny(t, "A1")
	revB, _ := revokeAny(t, "B")
	e.pki.dsc[string(fixtureDer(t, "A1", "dsc.der"))] = "DE"
	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "DE"

	end := e.ctx.BlockTime().Add(24 * time.Hour)
	p := e.openExpedited(t, 1, end)
	p.Messages = []*codectypes.Any{revA, revB}
	require.NoError(t, e.gov.SetProposal(e.ctx, p))
	e.enterVoting(t, 1)
	cp.calls = 0

	// One human ratifies (1 yes / 0 no >= 3/4).
	e.voteAll(t, 1, types.VOTE_OPTION_YES, "attacker")
	require.Equal(t, 0, cp.calls, "stored subjects: no pki work per vote")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.NoError(t, gov.EndBlocker(e.ctx, e.gov)) // stake: no quorum -> expedited demoted

	after, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, after.Status)
	require.False(t, after.Expedited, "x/gov demoted it")
	has, err := e.k.Subjects.Has(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, has, "subjects kept while the proposal is still in voting")

	// A trust-store change mid-vote does not move the exclusions.
	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "FR"
	_, v := e.addr(t, "v2", "n2")
	_, err = e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(v), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "DE"

	// No vote attempt redoes the pki walk.
	cp.calls = 0
	for i := 0; i < 10; i++ {
		_, err := voteProposalAction{e.k}.CheckPrivateAction(e.ctx, &types.MsgVoteProposal{Membership: voter("junk"), ProposalId: 1, Option: types.VOTE_OPTION_YES})
		require.NoError(t, err)
	}
	require.Zero(t, cp.calls, "classification happens once, not per vote")

	// The regular round ends (no stake quorum): rejected, subjects forgotten.
	after, err = e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	e.ctx = e.ctx.WithBlockTime(after.VotingEndTime.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	require.NoError(t, gov.EndBlocker(e.ctx, e.gov))
	after, err = e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.NotEqual(t, v1.StatusVotingPeriod, after.Status)
	has, err = e.k.Subjects.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, has, "subjects go once voting has ended")
}

// A voting proposal whose subjects were never fixed is refused, not
// classified per vote.
func TestUnfixedSubjectsRefused(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	require.NoError(t, e.k.Subjects.Remove(e.ctx, 1))
	_, v := e.addr(t, "voter", "n")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(v), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.ErrorIs(t, err, types.ErrProposalNotVoting)
}

// Subjects left behind by a swallowed hook error are swept once x/gov has
// ended the proposal.
func TestEndedProposalSubjectsSwept(t *testing.T) {
	e := newTestEnv(t)
	p := e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	p.Status = v1.StatusRejected
	require.NoError(t, e.gov.SetProposal(e.ctx, p))
	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 10))
	has, err := e.k.Subjects.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, has)
}

// Audit 6 D-L-AS1: the Subjects walk resumes where it stopped, so subjects
// of an ended proposal sorted after `limit` live ones are reached.
func TestSubjectsSweepResumes(t *testing.T) {
	e := newTestEnv(t)
	for _, id := range []uint64{1, 2} {
		e.openProposal(t, id, e.ctx.BlockTime().Add(time.Hour))
	}
	p, err := e.gov.Proposals.Get(e.ctx, 2)
	require.NoError(t, err)
	p.Status = v1.StatusRejected
	require.NoError(t, e.gov.SetProposal(e.ctx, p))

	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 1)) // looks at 1 only
	has, err := e.k.Subjects.Has(e.ctx, 2)
	require.NoError(t, err)
	require.True(t, has)
	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 1)) // resumes at 2
	has, err = e.k.Subjects.Has(e.ctx, 2)
	require.NoError(t, err)
	require.False(t, has, "forgotten")
	has, err = e.k.Subjects.Has(e.ctx, 1)
	require.NoError(t, err)
	require.True(t, has, "a live proposal's subjects stay")
}

// Audit 4 C6: a proposal's subjects, fixed as it entered voting, are
// exported and imported as they are, not recomputed against the relaunch's
// trust store.
func TestSubjectsCarriedThroughGenesis(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	fixed := types.ProposalSubjects{ExcludedCountry: "NZ"}
	require.NoError(t, e.k.Subjects.Set(e.ctx, 1, fixed))

	exported, err := e.k.ExportGenesis(e.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())
	require.Equal(t, []types.ProposalSubjectsEntry{{ProposalId: 1, Subjects: fixed}}, exported.ProposalSubjects)

	fresh := newTestEnv(t)
	fresh.openProposal(t, 1, fresh.ctx.BlockTime().Add(time.Hour))
	require.NoError(t, fresh.k.Subjects.Remove(fresh.ctx, 1)) // as if never classified
	require.NoError(t, fresh.k.InitGenesis(fresh.ctx, *exported))
	got, err := fresh.k.Subjects.Get(fresh.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, fixed, got, "the imported subjects stand, not a reclassification")

	bad := *exported
	bad.ProposalSubjects = []types.ProposalSubjectsEntry{{ProposalId: 1, Subjects: types.ProposalSubjects{ExcludedCountry: "NZ", Refusal: "x"}}}
	require.Error(t, bad.Validate())
	bad.ProposalSubjects = []types.ProposalSubjectsEntry{{ProposalId: 1}, {ProposalId: 1}}
	require.Error(t, bad.Validate())
}
