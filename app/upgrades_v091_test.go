package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// v0.9.1 replaces every key v0.7.0 installed.
//
// It used to also require each key to equal the zk/ultrahonk fixture's. The
// fixtures follow the current circuits, which v0.9.2 changed, so that check
// now lives in TestV092EmbedsTheRecompiledVerifyingKeys; v0.9.1's keys are
// history and only have to be well-formed and new.
func TestV091EmbedsTheRecompiledVerifyingKeys(t *testing.T) {
	entries, err := v091Assets.ReadDir(v091VerifyingKeyDir)
	if err != nil {
		t.Fatalf("read embedded verifying keys: %v", err)
	}
	old, err := v070Assets.ReadDir(v070VerifyingKeyDir)
	if err != nil {
		t.Fatalf("read v0.7.0 verifying keys: %v", err)
	}
	if len(entries) != len(old) {
		t.Fatalf("v0.9.1 embeds %d keys, v0.7.0 installed %d", len(entries), len(old))
	}

	for _, e := range entries {
		algo := strings.TrimSuffix(e.Name(), ".vk.b64")
		vk := decodeEmbeddedKey(t, v091Assets.ReadFile, path.Join(v091VerifyingKeyDir, e.Name()))
		prev := decodeEmbeddedKey(t, v070Assets.ReadFile, path.Join(v070VerifyingKeyDir, e.Name()))
		if bytes.Equal(vk, prev) {
			t.Fatalf("%s: the v0.9.1 key is v0.7.0's; the circuit was not recompiled", algo)
		}

	}
}

func decodeEmbeddedKey(t *testing.T, read func(string) ([]byte, error), name string) []byte {
	t.Helper()
	raw, err := read(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	vk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s is not valid base64: %v", name, err)
	}
	if len(vk) == 0 {
		t.Fatalf("%s decodes to nothing", name)
	}
	return vk
}

// The handler, run for real against earth-1's genesis state: every verifying
// key it installs must be the embedded one, and the allocation precondition
// must pass on a ledger that balances.
//
// networks/genesis.json is the chain's actual starting state, so its params
// carry exactly the circuit set earth-1 has — the same set the handler's
// both-directions check compares against at the upgrade height.
func TestV091HandlerSwapsEveryKeyOnGenesisState(t *testing.T) {
	app, ctx := initPinnedGenesis(t)

	before, err := app.PersonhoodKeeper.Params.Get(ctx)
	require.NoError(t, err)

	_, err = upgradeV091(app)(ctx, upgradetypes.Plan{Name: "v0.9.1"}, app.ModuleManager.GetVersionMap())
	require.NoError(t, err)

	after, err := app.PersonhoodKeeper.Params.Get(ctx)
	require.NoError(t, err)
	require.Len(t, after.VerifyingKeys, len(before.VerifyingKeys))
	for algo, vk := range after.VerifyingKeys {
		want := decodeEmbeddedKey(t, v091Assets.ReadFile, path.Join(v091VerifyingKeyDir, algo+".vk.b64"))
		require.Equal(t, want, vk, "%s was not replaced", algo)
		require.NotEqual(t, before.VerifyingKeys[algo], vk, "%s kept its old key", algo)
	}
	// Nothing else in the params moved.
	after.VerifyingKeys, before.VerifyingKeys = nil, nil
	require.Equal(t, before, after)
}

func initPinnedGenesis(t *testing.T) (*App, sdk.Context) {
	t.Helper()
	raw, err := os.ReadFile("../networks/genesis.json")
	require.NoError(t, err)
	var doc struct {
		ChainID   string          `json:"chain_id"`
		AppState  json.RawMessage `json:"app_state"`
		Consensus struct {
			Params json.RawMessage `json:"params"`
		} `json:"consensus"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	// A temp home, or wasmvm makes its cache under the package directory.
	opts := simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()}
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, opts, baseapp.SetChainID(doc.ChainID))

	var cp cmtproto.ConsensusParams
	var cpJSON cmttypes.ConsensusParams
	require.NoError(t, cmtjson.Unmarshal(doc.Consensus.Params, &cpJSON))
	cp = cpJSON.ToProto()

	now := time.Now().UTC()
	_, err = app.InitChain(&abci.RequestInitChain{
		ChainId:         doc.ChainID,
		Time:            now,
		InitialHeight:   1,
		ConsensusParams: &cp,
		AppStateBytes:   doc.AppState,
	})
	require.NoError(t, err)
	// InitChain's writes land in the first block's state; commit it so a plain
	// context reads them.
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1, Time: now})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)

	ctx := app.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: doc.ChainID, Height: 2, Time: now})
	return app, ctx
}
