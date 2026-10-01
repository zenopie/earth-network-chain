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

func fixtureDer(t *testing.T, fixture, file string) []byte {
	t.Helper()
	der, err := os.ReadFile("../../personhood/testdata/passports/" + fixture + "/" + file)
	require.NoError(t, err)
	return der
}

func revokeCscaAny(t *testing.T, fixture string) *codectypes.Any {
	t.Helper()
	a, err := codectypes.NewAnyWithValue(&pkitypes.MsgRevokeCsca{Authority: "gov", CertificateDer: fixtureDer(t, fixture, "csca.der")})
	require.NoError(t, err)
	return a
}

// A vote on a proposal revoking one Document Signer proves a leaf not made
// under it (excluded_dsc). A proposal whose revocations all belong to one
// country excludes that country (excluded_country). One spanning countries,
// or with an unknown-country revocation among several, cannot be voted on.
func TestRevocationProposalExcludesItsSubjects(t *testing.T) {
	e := newTestEnv(t)
	revA, commitmentA := revokeAny(t, "A1")
	revB, _ := revokeAny(t, "B")
	revC, _ := revokeAny(t, "C1")
	e.pki.dsc[string(fixtureDer(t, "A1", "dsc.der"))] = "DE"
	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "DE"
	e.pki.dsc[string(fixtureDer(t, "C1", "dsc.der"))] = "FR"
	e.pki.csca[string(fixtureDer(t, "A1", "csca.der"))] = "DE"
	cscaA := revokeCscaAny(t, "A1")
	cscaB := revokeCscaAny(t, "B") // unknown country

	_, bystander := e.addr(t, "bystander", "n")
	vote := func(id uint64) error {
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(bystander), ProposalId: id, Option: types.VOTE_OPTION_YES})
		return err
	}
	end := e.ctx.BlockTime().Add(time.Hour)
	id := uint64(0)
	propose := func(msgs ...*codectypes.Any) uint64 {
		id++
		p := e.openProposal(t, id, end)
		p.Messages = msgs
		require.NoError(t, e.gov.SetProposal(e.ctx, p))
		return id
	}
	last := func() (string, string) {
		st := e.humans.statements[len(e.humans.statements)-1]
		return string(privacy.FieldBytes(st.ExcludedDsc)), string(privacy.FieldBytes(st.ExcludedCountry))
	}
	zero := string(make([]byte, 32))
	de := string(privacy.FieldBytes(privacy.CountryField("DE")))

	// One DSC: that signer, not its country.
	p := propose(revA)
	require.NoError(t, vote(p))
	dsc, country := last()
	require.Equal(t, string(commitmentA), dsc)
	require.Equal(t, zero, country)
	st := e.humans.statements[len(e.humans.statements)-1]
	require.Equal(t, privacy.ProposalScope(p, 0), st.Scope)

	// Two DSCs of one country, or a DSC and its country's CSCA, or the CSCA
	// alone: the country.
	for _, msgs := range [][]*codectypes.Any{{revA, revB}, {revA, cscaA}, {cscaA}} {
		require.NoError(t, vote(propose(msgs...)))
		dsc, country := last()
		require.Equal(t, zero, dsc)
		require.Equal(t, de, country)
	}

	// A DSC no CSCA can have issued constrains nothing among several.
	revX, _ := revokeAny(t, "C2")
	require.NoError(t, vote(propose(revA, revX)))
	_, country = last()
	require.Equal(t, de, country)

	// Across countries, an unknown-country CSCA, or an unknown-country DSC
	// among several: no vote at all.
	e.pki.dsc[string(fixtureDer(t, "A2", "dsc.der"))] = ""
	revU, _ := revokeAny(t, "A2")
	for _, msgs := range [][]*codectypes.Any{{revA, revC}, {cscaB}, {revA, revU}} {
		require.ErrorIs(t, vote(propose(msgs...)), types.ErrTooManySubjects)
	}
}

// A proposal's subjects are fixed as it enters voting: a later change to the
// trust store does not move them mid-vote. They go when the proposal does.
func TestProposalSubjectsFixedAtVotingStart(t *testing.T) {
	e := newTestEnv(t)
	revA, _ := revokeAny(t, "A1")
	revB, _ := revokeAny(t, "B")
	e.pki.dsc[string(fixtureDer(t, "A1", "dsc.der"))] = "DE"
	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "DE"
	p := e.openProposal(t, 1, e.ctx.BlockTime().Add(time.Hour))
	p.Messages = []*codectypes.Any{revA, revB}
	require.NoError(t, e.gov.SetProposal(e.ctx, p))
	require.NoError(t, e.k.GovHooks().AfterProposalDeposit(e.ctx, 1, nil))

	e.pki.dsc[string(fixtureDer(t, "B", "dsc.der"))] = "FR"
	_, v := e.addr(t, "voter", "n")
	_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{Membership: voter(v), ProposalId: 1, Option: types.VOTE_OPTION_YES})
	require.NoError(t, err)
	st := e.humans.statements[len(e.humans.statements)-1]
	require.Equal(t, privacy.CountryField("DE"), st.ExcludedCountry)

	// Cancelled (deleted by x/gov): its subjects are swept with its ballot.
	require.NoError(t, e.gov.DeleteProposal(e.ctx, 1))
	require.NoError(t, e.k.closeOrphanedBallots(e.ctx, 10))
	has, err := e.k.Subjects.Has(e.ctx, 1)
	require.NoError(t, err)
	require.False(t, has)
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
