package keeper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
)

// Audit 4 C6: a proposal's subjects, fixed as it entered voting, are
// exported and imported as they are, not recomputed against the relaunch's
// trust store.
func TestAudit4SubjectsCarriedThroughGenesis(t *testing.T) {
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
