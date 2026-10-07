package app

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	gateway "github.com/cosmos/gogogateway"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/earth-network/earth/app/resultcap"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
)

// Result bytes are counted at their worst-case JSON size (resultcap, "How
// bytes are counted"), so that gas and the caps bound the node's JSON answers
// (RPC block_results / tx, LCD txs/{hash}) and not only what is stored.

// jsonLenSamples: every byte alone, the escapes, the runes the encoders
// treat specially, invalid UTF-8, and seeded random mixes of all of them.
func jsonLenSamples() []string {
	out := []string{"", "plain ascii: earth1abc/0-9_+=.,;:!?'()[]{}~`|^%$#@*", "\x7f",
		"<>&", `"\`, "\b\f\n\r\t", "\x00\x01\x1f", "  ", "é日本🙂�",
		"\xff", "\xfe\xff", "\xe2\x80", "\xe2\x80\xa8\xe2\x80", "\xed\xa0\x80", "\xc0\xaf", "\xf4\x90\x80\x80"}
	for c := 0; c < 256; c++ {
		out = append(out, string([]byte{byte(c)}), "a"+string([]byte{byte(c)})+"b")
	}
	alphabet := []string{"a", "Z", "0", " ", "<", ">", "&", `"`, `\`, "\n", "\x01", "\x7f", "é", "日", "🙂",
		" ", " ", "\xff", "\xe2\x80", "\xf0\x9f", "�"}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for j := r.Intn(40); j > 0; j-- {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		out = append(out, b.String())
	}
	return out
}

// TestResultCapJSONLen: jsonLen is exactly what each answer's encoder makes
// of an event string, and no encoder makes more: encoding/json (the base of
// both), CometBFT's RPC encoding of an ExecTxResult (gogoproto jsonpb inside
// cmtjson, block_results and tx), the SDK's LCD gateway marshaler of a
// TxResponse (gogoproto jsonpb, txs/{hash}); protojson escapes less.
func TestResultCapJSONLen(t *testing.T) {
	lcd := &gateway.JSONPb{EmitDefaults: true, OrigName: true, AnyResolver: codectypes.NewInterfaceRegistry()}
	rpcLen := func(v string) int {
		bz, err := cmtjson.Marshal(&abci.ExecTxResult{Events: []abci.Event{{Type: "t", Attributes: []abci.EventAttribute{{Key: "k", Value: v, Index: true}}}}})
		require.NoError(t, err)
		return len(bz)
	}
	lcdLen := func(v string) int {
		bz, err := lcd.Marshal(&sdk.TxResponse{Events: []abci.Event{{Type: "t", Attributes: []abci.EventAttribute{{Key: "k", Value: v, Index: true}}}}})
		require.NoError(t, err)
		return len(bz)
	}
	rpc0, lcd0 := rpcLen(""), lcdLen("")
	for _, s := range jsonLenSamples() {
		bz, err := json.Marshal(s)
		require.NoError(t, err)
		want := len(bz) - 2
		require.Equal(t, uint64(want), jsonLen(s), "encoding/json %q", s)
		require.Equal(t, want, rpcLen(s)-rpc0, "RPC %q", s)
		require.Equal(t, want, lcdLen(s)-lcd0, "LCD %q", s)
		if utf8.ValidString(s) {
			bz, err := protojson.Marshal(structpb.NewStringValue(s))
			require.NoError(t, err)
			require.LessOrEqual(t, len(bz)-2, want, "protojson %q", s)
		}
		require.LessOrEqual(t, jsonLen(s), uint64(resultcap.JSONEscapeBytes*len(s)))
	}
	require.Equal(t, uint64(6*1000), jsonLen(strings.Repeat("<", 1000)))
	require.Equal(t, uint64(1000), jsonLen(strings.Repeat("A", 1000)))
}

// TestResultCapJSONCounting: an escape-free ASCII event costs what it did
// (proto size plus overheads), every byte that JSON escapes counts as its
// escape, and a response's bytes count twice (hex in the LCD's data).
func TestResultCapJSONCounting(t *testing.T) {
	plain := func(evs []abci.Event) uint64 { // the round-8 count
		var n uint64
		for _, e := range evs {
			n += uint64(e.Size()) + resultEventOverhead + resultAttributeOverhead*uint64(len(e.Attributes))
		}
		return n
	}
	ordinary := sdk.Events{
		sdk.NewEvent("transfer", sdk.NewAttribute("recipient", "earth1qyqszqgpqyqszqgpqyqszqgpqyqszqgp8apuk6"),
			sdk.NewAttribute("sender", "earth1zgpqyqszqgpqyqszqgpqyqszqgpqyqszrh3s4w"), sdk.NewAttribute("amount", "1000uerth")),
		sdk.NewEvent("message", sdk.NewAttribute("action", "/cosmos.bank.v1beta1.MsgSend"), sdk.NewAttribute("module", "bank")),
		sdk.NewEvent("shielded_note", sdk.NewAttribute("ciphertext", "q83v+/9=AbC"), sdk.NewAttribute("nullifier", "0a1b2c")),
	}.ToABCIEvents()
	require.Equal(t, plain(ordinary), eventBytes(ordinary))

	lt := eventsOfSize(0)
	lt[0].Attributes[0].Value = strings.Repeat("<", 1000)
	require.Equal(t, plain(lt)+5*1000, eventBytes(lt))
	lt[0].Type, lt[0].Attributes[0].Key = "&", "\n"
	require.Equal(t, plain(lt)+5*1000+5+1, eventBytes(lt))

	resp, err := codectypes.NewAnyWithValue(&banktypes.MsgSendResponse{})
	require.NoError(t, err)
	res := &sdk.Result{MsgResponses: []*codectypes.Any{resp}}
	require.Equal(t, uint64(resultcap.ResponseByteWeight*resp.Size()+resultEventOverhead+resultMsgOverhead), resultBytes(res))

	// The per-msg cap, through capResult: '<' reaches it at a sixth of the
	// raw size that 'x' needs.
	emit := func(fill byte, n int) baseapp.MsgServiceHandler {
		return func(ctx sdk.Context, _ sdk.Msg) (*sdk.Result, error) {
			return &sdk.Result{Events: []abci.Event{{Type: "blob", Attributes: []abci.EventAttribute{{Key: "v", Value: strings.Repeat(string(fill), n)}}}}}, nil
		}
	}
	run := func(fill byte, n int) error {
		_, err := capResult(emit(fill, n))(capCtx(&resultMeter{}, storetypes.NewInfiniteGasMeter()), nil)
		return err
	}
	sixth := maxMsgResultBytes / resultcap.JSONEscapeBytes
	require.NoError(t, run('<', sixth-1024))
	require.ErrorIs(t, run('<', sixth+1024), sdkerrors.ErrTxTooLarge)
	require.NoError(t, run('x', sixth+1024))
	require.NoError(t, run('x', maxMsgResultBytes-1024))
	require.ErrorIs(t, run('x', maxMsgResultBytes+1024), sdkerrors.ErrTxTooLarge)

	// Error texts are cut by their JSON size too.
	cut := truncateText(strings.Repeat("<", 4096))
	require.LessOrEqual(t, jsonLen(cut[:strings.Index(cut, "...")]), uint64(maxErrorLogBytes))
	require.Equal(t, maxErrorLogBytes/resultcap.JSONEscapeBytes, strings.Index(cut, "..."))
	cut = truncateText(strings.Repeat("a", 4096))
	require.Equal(t, maxErrorLogBytes, strings.Index(cut, "..."), "ASCII errors are cut where they were")
	require.Equal(t, strings.Repeat("a", maxErrorLogBytes), truncateText(strings.Repeat("a", maxErrorLogBytes)))
	cut = truncateText(strings.Repeat("é", 4096))
	require.True(t, utf8.ValidString(cut))
}

// TestResultCapJSONWeighted, end to end: a contract's '<' output fails its tx
// at a sixth of the raw size an 'A' output may have; under the cap it pays
// for its JSON size; the chain's own flows count what they did; and all of
// it is deterministic.
func TestResultCapJSONWeighted(t *testing.T) {
	sixth := resultcap.MaxTxResultBytes / resultcap.JSONEscapeBytes
	type outcome struct {
		results [][]byte
	}
	run := func(t *testing.T) outcome {
		e := newCapEnv(t, "result-cap-json")
		user := e.bech(e.userAddr())
		under := e.deploy("lt-under", buildOutputContract(attrPrefix, sixth-4096, '<', attrSuffix, false))
		over := e.deploy("lt-over", buildOutputContract(attrPrefix, sixth+4096, '<', attrSuffix, false))
		plainOver := e.deploy("a-over", buildOutputContract(attrPrefix, sixth+4096, 'A', attrSuffix, false))
		exec := func(c string) *abci.ExecTxResult {
			const gas = 40_000_000
			return e.finalize(e.signedTx(gas, e.gasFee(gas), &wasmtypes.MsgExecuteContract{Sender: user, Contract: c, Msg: []byte("{}")})).TxResults[0]
		}
		var out outcome
		keep := func(r *abci.ExecTxResult) {
			bz, err := r.Marshal()
			require.NoError(t, err)
			out.results = append(out.results, bz)
		}

		r := exec(under)
		requireOK(t, r)
		require.Less(t, resultSize(t, r), sixth)
		require.GreaterOrEqual(t, r.GasUsed, int64(resultcap.JSONEscapeBytes*(sixth-4096)-resultFreeBytes)*resultGasPerByte)
		keep(r)

		r = exec(over)
		require.Equal(t, sdkerrors.ErrTxTooLarge.ABCICode(), r.Code, r.Log)
		require.Contains(t, r.Log, "over the 1048576-byte limit")
		require.Less(t, resultSize(t, r), 4<<10)
		keep(r)

		r = exec(plainOver)
		requireOK(t, r)
		require.Greater(t, resultSize(t, r), sixth)
		keep(r)

		// The chain's own flows: no string of theirs is escaped, so the
		// count of every event is the round-8 count.
		s := shieldedtest.Default()
		fb := e.shieldAll(s)
		send := e.finalize(e.sendTx(s, shieldedtest.Send2))
		requireOK(t, send.TxResults[0])
		bank := e.finalize(e.signedTx(200_000, e.fee(1_000), &banktypes.MsgSend{FromAddress: user, ToAddress: e.bech(sdk.AccAddress([]byte("json-weight-receiver"))), Amount: e.fee(7)}))
		requireOK(t, bank.TxResults[0])
		for _, res := range []*abci.ExecTxResult{fb.TxResults[0], send.TxResults[0], bank.TxResults[0]} {
			for _, ev := range res.Events {
				require.Equal(t, uint64(len(ev.Type)), jsonLen(ev.Type), ev.Type)
				for _, a := range ev.Attributes {
					require.Equal(t, uint64(len(a.Key)), jsonLen(a.Key), a.Key)
					require.Equal(t, uint64(len(a.Value)), jsonLen(a.Value), "%s/%s", ev.Type, a.Key)
				}
			}
			keep(res)
		}
		return out
	}
	a := run(t)
	b := run(t)
	require.Equal(t, a.results, b.results, "same txs on two fresh chains: identical stored results")
}
