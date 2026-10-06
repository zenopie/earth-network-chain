package app

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	assemblymodule "github.com/earth-network/earth/x/assembly/module"
	assemblytypes "github.com/earth-network/earth/x/assembly/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// TestAbsentAssemblySectionValidates is an operator-facing guard, not a unit
// test of the module.
//
// A genesis file may carry no assembly section, and the module manager
// handles that on the way in: InitGenesis skips a module with no genesis data,
// so the chamber starts empty.
//
// Validation is the one path that does not skip; BasicManager.ValidateGenesis
// passes genesisData[name] through even when it is nil. Refusing nil there
// breaks `earthd genesis validate-genesis`, `gentx` and `collect-gentxs` against
// such a file, and it failed with a bare "EOF" naming nothing actionable.
//
// Any future module added after a genesis is pinned has the same obligation.
func TestAbsentAssemblySectionValidates(t *testing.T) {
	// Exactly what BasicManager.ValidateGenesis hands a module that is absent.
	var module assemblymodule.AppModule
	require.NoError(t, module.ValidateGenesis(nil, nil, nil),
		"an absent section must validate as an empty chamber")
}

type allTripped struct{}

func (allTripped) IsAllowed(context.Context, string) (bool, error) { return false, nil }

// Audit 5 L-AS2: a tripped breaker never stops the chamber's votes (every gov
// proposal needs them), and still stops everything else.
func TestChamberVotesPassTheBreaker(t *testing.T) {
	b := chamberExemptBreaker{allTripped{}}
	for _, m := range []sdk.Msg{&assemblytypes.MsgVoteProposal{}, &assemblytypes.MsgVoteRemoval{}} {
		ok, err := b.IsAllowed(context.Background(), sdk.MsgTypeURL(m))
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := b.IsAllowed(context.Background(), sdk.MsgTypeURL(&shieldedtypes.MsgSend{}))
	require.NoError(t, err)
	require.False(t, ok)
}
