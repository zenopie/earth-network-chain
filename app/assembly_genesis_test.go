package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	assemblymodule "github.com/earth-network/earth/x/assembly/module"
)

// TestAbsentAssemblySectionValidates is an operator-facing guard, not a unit
// test of the module.
//
// x/assembly was added to a chain that already had a genesis file. The earth-1
// relaunch genesis (v0.9.3) carries an assembly section, but any genesis cut
// before v0.9.0 does not, and the module manager handles that on the way in:
// InitGenesis skips a module with no genesis data, so the chamber starts empty.
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
