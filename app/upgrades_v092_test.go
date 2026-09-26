package app

import (
	"bytes"
	"os"
	"path"
	"strings"
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/stretchr/testify/require"

	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
)

// v0.9.2's keys are the current circuits': each must equal the zk/ultrahonk
// fixture that scripts/regen-poa-fixtures.sh produced with a real proof, and
// differ from v0.9.1's.
func TestV092EmbedsTheRecompiledVerifyingKeys(t *testing.T) {
	entries, err := v092Assets.ReadDir(v092VerifyingKeyDir)
	require.NoError(t, err)
	prev, err := v091Assets.ReadDir(v091VerifyingKeyDir)
	require.NoError(t, err)
	require.Len(t, entries, len(prev))

	for _, e := range entries {
		algo := strings.TrimSuffix(e.Name(), ".vk.b64")
		vk := decodeEmbeddedKey(t, v092Assets.ReadFile, path.Join(v092VerifyingKeyDir, e.Name()))
		old := decodeEmbeddedKey(t, v091Assets.ReadFile, path.Join(v091VerifyingKeyDir, e.Name()))
		require.False(t, bytes.Equal(vk, old), "%s: the v0.9.2 key is v0.9.1's; the circuit was not recompiled", algo)

		fixture, err := os.ReadFile(path.Join("..", "zk", "ultrahonk", "testdata", algo, "vk"))
		require.NoError(t, err)
		require.True(t, bytes.Equal(vk, fixture), "%s: embedded key differs from the regenerated fixture's", algo)
	}
}

// Every allowlisted type URL names a message this app actually registers. A
// typo would silently allow nothing for that message.
func TestV092IcaAllowlistResolves(t *testing.T) {
	app, _ := initPinnedGenesis(t)
	for _, url := range icaAllowMessages {
		_, err := app.InterfaceRegistry().Resolve(url)
		require.NoError(t, err, "%s is not a registered message", url)
	}
}

// The handler run for real against earth-1's genesis state, after v0.9.1.
func TestV092HandlerOnGenesisState(t *testing.T) {
	app, ctx := initPinnedGenesis(t)
	vm := app.ModuleManager.GetVersionMap()
	_, err := upgradeV091(app)(ctx, upgradetypes.Plan{Name: "v0.9.1"}, vm)
	require.NoError(t, err)

	_, err = upgradeV092(app)(ctx, upgradetypes.Plan{Name: "v0.9.2"}, vm)
	require.NoError(t, err)

	params, err := app.PersonhoodKeeper.Params.Get(ctx)
	require.NoError(t, err)
	for algo, vk := range params.VerifyingKeys {
		want := decodeEmbeddedKey(t, v092Assets.ReadFile, path.Join(v092VerifyingKeyDir, algo+".vk.b64"))
		require.Equal(t, want, vk, "%s was not replaced", algo)
	}
	require.Equal(t, uint64(personhoodtypes.DefaultProofVerificationGas), params.ProofVerificationGas)
	require.Equal(t, uint64(personhoodtypes.DefaultDscVerificationGas), params.DscVerificationGas)
	require.Equal(t, uint64(personhoodtypes.DefaultNetworkDailyRegistrationFloor), params.NetworkDailyRegistrationFloor)
	require.NoError(t, params.Validate())

	ica := app.ICAHostKeeper.GetParams(ctx)
	require.Equal(t, icaAllowMessages, ica.AllowMessages)

	n, err := app.PersonhoodKeeper.RetireAllRegistrations(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "the handler already retired everything")
}
