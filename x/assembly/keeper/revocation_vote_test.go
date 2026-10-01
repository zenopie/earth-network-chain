package keeper

import (
	"os"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	"github.com/earth-network/earth/zk/privacy"
)

func revokeAny(t *testing.T, fixture string) (*codectypes.Any, []byte) {
	t.Helper()
	der, err := os.ReadFile("../../personhood/testdata/passports/" + fixture + "/dsc.der")
	require.NoError(t, err)
	cert, err := certs.ParseCert(der)
	require.NoError(t, err)
	c, err := certs.DscCommitmentOf(cert.PublicKey)
	require.NoError(t, err)
	a, err := codectypes.NewAnyWithValue(&pkitypes.MsgRevokeDsc{Authority: "gov", CertificateDer: der})
	require.NoError(t, err)
	b := c.Bytes()
	return a, b[:]
}

// A vote on a proposal revoking a Document Signer proves a leaf not made under
// it: the signer is the statement's excluded_dsc. A proposal revoking two
// cannot be voted on at all.
func TestRevocationProposalExcludesItsSigner(t *testing.T) {
	e := newTestEnv(t)
	end := e.ctx.BlockTime().Add(time.Hour)
	proposal := e.openProposal(t, 1, end)
	revoke, commitment := revokeAny(t, "A1")
	proposal.Messages = []*codectypes.Any{revoke}
	require.NoError(t, e.gov.SetProposal(e.ctx, proposal))

	_, bystander := e.addr(t, "bystander", "n")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(bystander), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	st := e.humans.statements[len(e.humans.statements)-1]
	require.Equal(t, commitment, privacy.FieldBytes(st.ExcludedDsc))
	require.Equal(t, privacy.ProposalScope(1, 0), st.Scope)
	require.Equal(t, proposal.VotingStartTime.Unix()-3600, st.MaxActivation)

	other, _ := revokeAny(t, "B")
	proposal.Messages = append(proposal.Messages, other)
	require.NoError(t, e.gov.SetProposal(e.ctx, proposal))
	_, err = e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(bystander), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.ErrorIs(t, err, types.ErrTooManySubjects)
}

// An ordinary proposal excludes no one.
func TestOrdinaryProposalsExcludeNoOne(t *testing.T) {
	e := newTestEnv(t)
	e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	_, v := e.addr(t, "voter", "n")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(v), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	require.True(t, e.humans.statements[0].ExcludedDsc.IsZero())
}
