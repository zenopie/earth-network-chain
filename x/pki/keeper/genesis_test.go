package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/pki/types"
)

// Revocation is the emergency response to a compromised Document Signer, and it
// is the one piece of this module's state nothing else can reconstruct: a CSCA
// comes back from its certificate, but "we decided to stop trusting this signer"
// exists only in the revocation set. An export that dropped it would silently
// re-trust every certificate governance had revoked — which is worse than never
// having revoked it, because the operator believes it is still revoked.
func TestGenesisRoundTripsRevocations(t *testing.T) {
	k, ctx := newKeeperForTest(t)

	revoked := [][]byte{{0x01, 0x02}, {0x03, 0x04}}
	original := types.GenesisState{Params: types.DefaultParams(), RevokedDscs: revoked}
	require.NoError(t, original.Validate())
	require.NoError(t, k.InitGenesis(ctx, original))

	for _, id := range revoked {
		has, err := k.RevokedDscs.Has(ctx, id)
		require.NoError(t, err)
		require.True(t, has, "dsc %x should be revoked after import", id)
	}

	got, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, revoked, got.RevokedDscs)
}

func TestGenesisRejectsADuplicateRevocation(t *testing.T) {
	gs := types.GenesisState{
		Params:      types.DefaultParams(),
		RevokedDscs: [][]byte{{0x01}, {0x01}},
	}
	require.ErrorContains(t, gs.Validate(), "revoked twice")
}
