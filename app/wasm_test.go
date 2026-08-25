package app

import (
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/log"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/cosmos/gogoproto/proto"
)

// TestWasmAcceptedQueriesAreRoutable checks every path in the contract query
// allowlist against the router that will actually serve it.
//
// The allowlist in app/wasm.go is a hand-written map of strings to constructors,
// and both halves fail silently when wrong. A mistyped path is simply never
// matched, so the contract gets "no route to query" from a chain that looks
// correctly configured. A path paired with the wrong response type is worse: the
// query runs, the bytes come back, and the codec unmarshals them into a message
// they were not encoded for — which usually succeeds, because protobuf field
// numbers collide happily across types, and hands the contract plausible
// nonsense. Neither shows up until someone deploys a contract.
//
// So: the path must route, and the response type must be the one the service
// declares for it. The second is derived from the naming convention every Cosmos
// Query service follows — "/pkg.Query/Method" answers with "pkg.QueryMethodResponse"
// — which holds for every module on this chain and every SDK module listed. A
// path that genuinely breaks the convention would need this test taught about
// it, and is worth the look that forces.
func TestWasmAcceptedQueriesAreRoutable(t *testing.T) {
	appOptions := make(simtestutil.AppOptionsMap, 0)
	appOptions[flags.FlagHome] = t.TempDir()

	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, appOptions, baseapp.SetChainID("earth-allowlist-test"))

	router := app.GRPCQueryRouter()

	for path, newResponse := range wasmAcceptedQueries() {
		t.Run(path, func(t *testing.T) {
			if router.Route(path) == nil {
				t.Fatalf("no gRPC route for allowlisted query %q; a contract calling it gets UnsupportedRequest", path)
			}

			service, method, ok := splitQueryPath(path)
			if !ok {
				t.Fatalf("malformed query path %q: want /pkg.Service/Method", path)
			}

			pkg := service[:strings.LastIndex(service, ".")]
			want := fmt.Sprintf("%s.Query%sResponse", pkg, method)
			if got := proto.MessageName(newResponse()); got != want {
				t.Errorf("allowlist pairs %q with response type %q, want %q", path, got, want)
			}
		})
	}
}

// splitQueryPath splits "/earth.dex.v1.Query/GetPool" into "earth.dex.v1.Query"
// and "GetPool".
func splitQueryPath(path string) (service, method string, ok bool) {
	trimmed, found := strings.CutPrefix(path, "/")
	if !found {
		return "", "", false
	}
	service, method, found = strings.Cut(trimmed, "/")
	if !found || service == "" || method == "" || !strings.Contains(service, ".") {
		return "", "", false
	}
	return service, method, true
}

// TestWasmIsPermissionless pins the two parameters that make CosmWasm on earth
// open to everyone. They are wasmd's defaults today, which is exactly why they
// are worth a test: a default can change under a dependency bump, and "anyone
// can deploy a contract" is a property of this chain rather than a property of
// whatever wasmd shipped.
//
// This is the compiled-in default, which is what a fresh `earthd init` writes.
// networks/genesis.json states the same values explicitly; networks/genesis_test.go
// checks that copy.
func TestWasmIsPermissionless(t *testing.T) {
	params := wasmtypes.DefaultParams()

	if got := params.CodeUploadAccess.Permission; got != wasmtypes.AccessTypeEverybody {
		t.Errorf("code upload access is %s, want Everybody: uploading code would need governance", got)
	}
	if got := params.InstantiateDefaultPermission; got != wasmtypes.AccessTypeEverybody {
		t.Errorf("default instantiate permission is %s, want Everybody", got)
	}
}
