package app

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	assemblymodule "github.com/earth-network/earth/x/assembly/module"
)

// TestPinnedGenesisValidatesWithoutAnAssemblySection is an operator-facing
// guard, not a unit test of the module.
//
// x/assembly was added to a chain that already had a genesis file, and
// networks/genesis.json is hash-pinned — earth-1's identity is that hash, so the
// file cannot gain a section for a module invented after it. The module manager
// handles this correctly on the way in: InitGenesis skips a module with no
// genesis data, so a syncing node starts the chamber empty.
//
// Validation is the one path that does not skip; BasicManager.ValidateGenesis
// passes genesisData[name] through even when it is nil. Refusing nil there
// breaks `earthd genesis validate-genesis`, `gentx` and `collect-gentxs` against
// the real genesis file — which is what a joining validator runs, and it failed
// with a bare "EOF" naming nothing actionable.
//
// Any future module added after the pin has the same obligation.
func TestPinnedGenesisValidatesWithoutAnAssemblySection(t *testing.T) {
	raw, err := os.ReadFile("../networks/genesis.json")
	require.NoError(t, err)

	var doc struct {
		AppState map[string]json.RawMessage `json:"app_state"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotContains(t, doc.AppState, "assembly",
		"the pinned genesis must not carry assembly state — editing it changes the genesis hash")

	// Exactly what BasicManager.ValidateGenesis hands a module that is absent.
	var module assemblymodule.AppModule
	require.NoError(t, module.ValidateGenesis(nil, nil, doc.AppState["assembly"]),
		"an absent section must validate as an empty chamber")
	require.NoError(t, module.ValidateGenesis(nil, nil, nil))
}
