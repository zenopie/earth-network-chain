package keeper

import (
	"os"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// A proposal to revoke a Document Signer is not voted on by the registrations
// that signer made. They are refused when they try to vote, so the running
// tally the proposal is decided on never counts them. Everyone else votes as
// normal.
func TestRevokedSignersRegistrationsDoNotVoteOnTheirRevocation(t *testing.T) {
	e := newTestEnv(t)

	der, err := os.ReadFile("../../personhood/keeper/testdata/lean_poa/dsc.der")
	require.NoError(t, err)
	cert, err := certs.ParseCert(der)
	require.NoError(t, err)
	c, err := certs.DscCommitmentOf(cert.PublicKey)
	require.NoError(t, err)
	commitment := c.Bytes()

	end := e.ctx.BlockTime().Add(time.Hour)
	proposal := e.openProposal(t, 1, end)
	revoke, err := codectypes.NewAnyWithValue(&pkitypes.MsgRevokeDsc{
		Authority:      "gov",
		CertificateDer: der,
	})
	require.NoError(t, err)
	proposal.Messages = []*codectypes.Any{revoke}
	require.NoError(t, e.gov.SetProposal(e.ctx, proposal))

	_, subject := e.addr(t, "subject", "null-subject")
	e.humans.dsc["null-subject"] = commitment[:]
	_, bystander := e.addr(t, "bystander", "null-bystander")
	e.humans.dsc["null-bystander"] = []byte("some other signer")

	_, err = e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: subject, ProposalId: 1, Option: types.VOTE_OPTION_NO})
	require.ErrorIs(t, err, types.ErrVoterIsSubject)

	_, err = e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: bystander, ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)

	tally, err := e.k.proposalTally(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.Tally{Yes: 1}, tally, "the refused vote must not reach the running tally")

	e.ctx = e.ctx.WithBlockTime(end.Add(time.Second))
	require.NoError(t, e.k.EndBlocker(e.ctx))
	got, err := e.gov.Proposals.Get(e.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, got.Status,
		"one YES and no admissible NO: carried, and left for x/gov")
}

// Proposals that revoke no one are unaffected: the same registration votes.
func TestOrdinaryProposalsExcludeNoOne(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	_, voter := e.addr(t, "voter", "null-voter")
	e.humans.dsc["null-voter"] = []byte("any signer")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Voter: voter, ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
}
