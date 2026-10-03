package cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// AUDIT3 M1 (TestPOC_GasCheckBlindToRevocations): a KeySet member is stored
// with an empty value, which abci_query's Value cannot tell from an absent
// key. gas-check must still see a revoked CSCA as revoked, exactly as the
// chain does. The node here is a real committed IAVL multistore answering
// abci_query (with real ics23 proofs when asked).
func TestPOC_GasCheckBlindToRevocations(t *testing.T) {
	storeKey := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	tc := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("t"))
	ctx := tc.Ctx
	cdc := gasCheckCodec()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	local := pkikeeper.NewKeeper(runtime.NewKVStoreService(storeKey), cdc, ac, authtypes.NewModuleAddress("gov"))

	revoked := []byte("some csca public key")
	other := []byte("another csca public key")
	require.NoError(t, local.RevokeCsca(ctx, revoked))
	got, err := local.IsCscaRevoked(ctx, revoked)
	require.NoError(t, err)
	require.True(t, got, "on chain: revoked")
	tc.CMS.Commit()
	height := tc.CMS.LastCommitID().Version

	queries := map[bool]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpctypes.RPCRequest
		require.NoError(t, json.Unmarshal(body, &req))
		var p struct {
			Path   string `json:"path"`
			Data   string `json:"data"`
			Height string `json:"height"`
			Prove  bool   `json:"prove"`
		}
		require.NoError(t, json.Unmarshal(req.Params, &p))
		data, err := hex.DecodeString(p.Data)
		require.NoError(t, err)
		queries[p.Prove]++
		q := tc.CMS.(storetypes.Queryable)
		resp, err := q.Query(&storetypes.RequestQuery{
			Path: strings.TrimPrefix(p.Path, "/store"), Data: data, Height: height, Prove: p.Prove,
		})
		require.NoError(t, err)
		res := &ctypes.ResultABCIQuery{Response: abci.ResponseQuery{Code: resp.Code, Value: resp.Value, Key: resp.Key,
			ProofOps: resp.ProofOps, Height: resp.Height}}
		out, _ := cmtjson.Marshal(res)
		b, _ := json.Marshal(rpctypes.RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: out})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	client, err := rpchttp.New(srv.URL, "/websocket")
	require.NoError(t, err)
	remote := remoteStoreService{&remoteKV{ctx: context.Background(), client: client, module: pkitypes.StoreKey, height: height}}
	viaGasCheck := pkikeeper.NewKeeper(remote, cdc, ac, authtypes.NewModuleAddress("gov"))

	got2, err := viaGasCheck.IsCscaRevoked(ctx, revoked)
	require.NoError(t, err)
	require.True(t, got2, "gas-check sees the revocation the chain sees")
	got3, err := viaGasCheck.IsCscaRevoked(ctx, other)
	require.NoError(t, err)
	require.False(t, got3, "an absent key stays absent")
	require.Positive(t, queries[true], "empty answers were re-asked with a proof")
}
