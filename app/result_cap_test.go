package app

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
)

// --- a contract that returns as many bytes as it is built to ---------------
//
// No contract in the tree emits a large result, and the attack needs one, so
// the tests build the smallest CosmWasm module that does: a bump allocator,
// an instantiate that returns an empty Response, and an execute that writes
// prefix + fill x 'A' + suffix into memory and returns it as its result
// (a ContractResult in JSON). The bytes are written by a loop, so the module
// stays a few hundred bytes however large the output.

func wasmULEB(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func wasmSLEB(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func wasmVec(items ...[]byte) []byte {
	out := wasmULEB(uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func wasmSection(id byte, body []byte) []byte {
	return append(append([]byte{id}, wasmULEB(uint64(len(body)))...), body...)
}

func wasmName(s string) []byte { return append(wasmULEB(uint64(len(s))), s...) }

func wasmConst(v int64) []byte { return append([]byte{0x41}, wasmSLEB(v)...) }

func wasmBody(locals int, code ...[]byte) []byte {
	var b []byte
	if locals > 0 {
		b = wasmVec(append(wasmULEB(uint64(locals)), 0x7f))
	} else {
		b = wasmVec()
	}
	for _, c := range code {
		b = append(b, c...)
	}
	b = append(b, 0x0b)
	return append(wasmULEB(uint64(len(b))), b...)
}

func wasmRegion(off, n int) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint32(b[0:], uint32(off))
	binary.LittleEndian.PutUint32(b[4:], uint32(n))
	binary.LittleEndian.PutUint32(b[8:], uint32(n))
	return b
}

func wasmData(off int, bz []byte) []byte {
	return append(append([]byte{0x00}, append(wasmConst(int64(off)), 0x0b)...), append(wasmULEB(uint64(len(bz))), bz...)...)
}

// outputContract is a module whose execute returns prefix + fill x 'A' +
// suffix as its ContractResult JSON.
func outputContract(prefix string, fill int, suffix string) []byte {
	const (
		instRegion = 16
		outRegion  = 32
		instJSON   = 64
		out        = 4096
	)
	inst := `{"ok":{"messages":[],"attributes":[],"events":[],"data":null}}`
	start := out + len(prefix)
	end := start + fill
	total := len(prefix) + fill + len(suffix)
	heap := (end + len(suffix) + 64) &^ 7
	pages := (heap + (1 << 20) + 65535) / 65536

	i32 := byte(0x7f)
	types := wasmVec(
		[]byte{0x60, 0x00, 0x00},                     // () -> ()
		[]byte{0x60, 0x01, i32, 0x01, i32},           // (i32) -> i32
		[]byte{0x60, 0x01, i32, 0x00},                // (i32) -> ()
		[]byte{0x60, 0x03, i32, i32, i32, 0x01, i32}, // (i32,i32,i32) -> i32
	)
	funcs := wasmVec([]byte{0}, []byte{1}, []byte{2}, []byte{3}, []byte{3})
	memory := wasmVec(append([]byte{0x00}, wasmULEB(uint64(pages))...))
	globals := wasmVec(append([]byte{i32, 0x01}, append(wasmConst(int64(heap)), 0x0b)...))
	exports := wasmVec(
		append(wasmName("memory"), 0x02, 0x00),
		append(wasmName("interface_version_8"), 0x00, 0x00),
		append(wasmName("allocate"), 0x00, 0x01),
		append(wasmName("deallocate"), 0x00, 0x02),
		append(wasmName("instantiate"), 0x00, 0x03),
		append(wasmName("execute"), 0x00, 0x04),
	)
	localGet := func(i byte) []byte { return []byte{0x20, i} }
	localSet := func(i byte) []byte { return []byte{0x21, i} }
	store := func(off byte) []byte { return []byte{0x36, 0x02, off} }
	store8 := []byte{0x3a, 0x00, 0x00}
	add := []byte{0x6a}

	allocate := wasmBody(1,
		[]byte{0x23, 0x00}, localSet(1), // r = heap
		[]byte{0x23, 0x00}, wasmConst(12), add, localGet(0), add, []byte{0x24, 0x00}, // heap = r+12+size
		localGet(1), localGet(1), wasmConst(12), add, store(0), // r.offset = r+12
		localGet(1), localGet(0), store(4), // r.capacity = size
		localGet(1), wasmConst(0), store(8), // r.length = 0
		localGet(1),
	)
	var tail [][]byte
	for j := 0; j < len(suffix); j++ {
		tail = append(tail, wasmConst(int64(end+j)), wasmConst(int64(suffix[j])), store8)
	}
	execCode := [][]byte{
		wasmConst(int64(start)), localSet(3),
		{0x02, 0x40, 0x03, 0x40},                               // block, loop
		localGet(3), wasmConst(int64(end)), {0x4f, 0x0d, 0x01}, // i >= end: br_if 1
		localGet(3), wasmConst('A'), store8,
		localGet(3), wasmConst(1), add, localSet(3),
		{0x0c, 0x00, 0x0b, 0x0b}, // br 0, end loop, end block
	}
	execCode = append(execCode, tail...)
	execCode = append(execCode, wasmConst(outRegion))
	code := wasmVec(
		wasmBody(0),
		allocate,
		wasmBody(0),
		wasmBody(0, wasmConst(instRegion)),
		wasmBody(1, execCode...),
	)
	data := wasmVec(
		wasmData(instRegion, wasmRegion(instJSON, len(inst))),
		wasmData(outRegion, wasmRegion(out, total)),
		wasmData(instJSON, []byte(inst)),
		wasmData(out, []byte(prefix)),
	)
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	for _, s := range []struct {
		id   byte
		body []byte
	}{{1, types}, {3, funcs}, {5, memory}, {6, globals}, {7, exports}, {10, code}, {11, data}} {
		mod = append(mod, wasmSection(s.id, s.body)...)
	}
	return mod
}

const (
	okPrefix   = `{"ok":{"messages":[],"attributes":[],"events":[],"data":"`
	okSuffix   = `"}}`
	attrPrefix = `{"ok":{"messages":[],"attributes":[{"key":"blob","value":"`
	attrSuffix = `"}],"events":[],"data":null}}`
	errPrefix  = `{"error":"`
	errSuffix  = `"}`
)

// --- end to end -----------------------------------------------------------

type capEnv struct {
	*shieldedEnv
	contracts map[string]string
}

// newCapEnv uploads and instantiates one contract per output shape.
func newCapEnv(t *testing.T, seed string) *capEnv {
	e := &capEnv{
		shieldedEnv: initShieldedEnvWith(t, shieldedEnvOpts{
			keySeed: seed, genesisTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		}),
		contracts: map[string]string{},
	}
	shapes := []struct {
		name           string
		prefix, suffix string
		fill           int
	}{
		{"small", okPrefix, okSuffix, 1 << 10},            // 1 KiB of data: in the free tier
		{"medium", okPrefix, okSuffix, 64 << 10},          // 64 KiB: paid
		{"huge", okPrefix, okSuffix, 1536 << 10},          // 1.5 MiB of data: over the cap
		{"huge-attr", attrPrefix, attrSuffix, 1536 << 10}, // 1.5 MiB in one event attribute
		{"long-error", errPrefix, errSuffix, 256 << 10},   // a 256 KiB error string
	}
	sender := e.bech(e.userAddr())
	var stores []sdk.Msg
	for _, s := range shapes {
		stores = append(stores, &wasmtypes.MsgStoreCode{Sender: sender, WASMByteCode: outputContract(s.prefix, s.fill, s.suffix)})
	}
	fb := e.finalize(e.signedTx(20_000_000, e.fee(100_000), stores...))
	requireOK(t, fb.TxResults[0])
	var ids []uint64
	for _, ev := range eventsOf(fb.TxResults[0].Events, wasmtypes.EventTypeStoreCode) {
		id, err := strconv.ParseUint(ev[wasmtypes.AttributeKeyCodeID], 10, 64)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Len(t, ids, len(shapes))
	var insts []sdk.Msg
	for i, s := range shapes {
		insts = append(insts, &wasmtypes.MsgInstantiateContract{
			Sender: sender, CodeID: ids[i], Label: s.name, Msg: []byte("{}"),
		})
	}
	fb = e.finalize(e.signedTx(5_000_000, e.fee(25_000), insts...))
	requireOK(t, fb.TxResults[0])
	addrs := eventsOf(fb.TxResults[0].Events, wasmtypes.EventTypeInstantiate)
	require.Len(t, addrs, len(shapes))
	for i, s := range shapes {
		e.contracts[s.name] = addrs[i][wasmtypes.AttributeKeyContractAddr]
	}
	return e
}

func (e *capEnv) exec(name string, funds sdk.Coins) *abci.ExecTxResult {
	e.t.Helper()
	msg := &wasmtypes.MsgExecuteContract{
		Sender: e.bech(e.userAddr()), Contract: e.contracts[name], Msg: []byte("{}"), Funds: funds,
	}
	fb := e.finalize(e.signedTx(10_000_000, e.fee(50_000), msg))
	require.Len(e.t, fb.TxResults, 1)
	return fb.TxResults[0]
}

func (e *capEnv) balance(addr string, denom string) int64 {
	e.t.Helper()
	res, err := e.app.BankKeeper.Balance(e.ctx(), &banktypes.QueryBalanceRequest{Address: addr, Denom: denom})
	require.NoError(e.t, err)
	return res.Balance.Amount.Int64()
}

func resultSize(t *testing.T, r *abci.ExecTxResult) int {
	bz, err := r.Marshal()
	require.NoError(t, err)
	return len(bz)
}

// TestResultCapContractOutput is R6-E-1 end to end: a contract's output past
// the cap fails its tx (state reverted, fee charged) and what the block stores
// is a few hundred bytes; under the cap, output past the free tier costs gas.
func TestResultCapContractOutput(t *testing.T) {
	e := newCapEnv(t, "result-cap")
	user := e.bech(e.userAddr())

	small := e.exec("small", nil)
	requireOK(t, small)
	medium := e.exec("medium", nil)
	requireOK(t, medium)
	require.Greater(t, resultSize(t, medium), 48<<10, "the medium output (64 KiB of base64, 48 KiB of data) is stored")
	// Its 48 KiB cost at least 20 gas per byte past the free tier.
	require.GreaterOrEqual(t, medium.GasUsed-small.GasUsed, int64((48<<10)-resultFreeBytes)*resultGasPerByte)

	for _, name := range []string{"huge", "huge-attr"} {
		t.Run(name, func(t *testing.T) {
			funds := sdk.NewCoins(sdk.NewInt64Coin("uerth", 1_000))
			before := e.balance(user, "uerth")
			r := e.exec(name, funds)
			require.Equal(t, sdkerrors.ErrTxTooLarge.ABCICode(), r.Code, r.Log)
			require.Equal(t, sdkerrors.ErrTxTooLarge.Codespace(), r.Codespace)
			require.Contains(t, r.Log, "over the 1048576-byte limit")
			require.Empty(t, r.Data)
			require.Less(t, resultSize(t, r), 4<<10, "a failed over-cap tx stores only its ante events and log")
			// Fee charged, funds not moved: the msg's state is reverted.
			require.Equal(t, before-50_000, e.balance(user, "uerth"))
			require.Zero(t, e.balance(e.contracts[name], "uerth"))
		})
	}

	t.Run("long error", func(t *testing.T) {
		r := e.exec("long-error", nil)
		require.NotZero(t, r.Code)
		require.Equal(t, wasmtypes.ModuleName, r.Codespace, "the contract's error keeps its codespace")
		require.Equal(t, wasmtypes.ErrExecuteFailed.ABCICode(), r.Code, r.Log)
		require.Contains(t, r.Log, "error truncated")
		require.Less(t, len(r.Log), maxErrorLogBytes+256)
		require.Less(t, resultSize(t, r), 4<<10)
	})

	t.Run("simulate", func(t *testing.T) {
		msg := &wasmtypes.MsgExecuteContract{Sender: user, Contract: e.contracts["huge"], Msg: []byte("{}")}
		_, _, err := e.app.Simulate(e.signedTx(10_000_000, e.fee(50_000), msg))
		require.ErrorIs(t, err, sdkerrors.ErrTxTooLarge)
	})
}

// TestResultCapDeterministic runs the same txs on two fresh chains: the
// stored results (code, gas, events, log, data) match byte for byte.
func TestResultCapDeterministic(t *testing.T) {
	run := func() [][]byte {
		e := newCapEnv(t, "result-cap-det")
		var out [][]byte
		for _, name := range []string{"small", "medium", "huge", "huge-attr", "long-error"} {
			bz, err := e.exec(name, nil).Marshal()
			require.NoError(t, err)
			out = append(out, bz)
		}
		return out
	}
	require.Equal(t, run(), run())
}

// --- the wrapper on its own -------------------------------------------------

func capCtx(meter *txResultMeter, gas storetypes.GasMeter) sdk.Context {
	ctx := sdk.NewContext(nil, cmtproto.Header{}, false, nil).WithGasMeter(gas).WithEventManager(sdk.NewEventManager())
	if meter != nil {
		ctx = ctx.WithValue(txResultMeterKey{}, meter)
	}
	return ctx
}

func eventsOfSize(n int) []abci.Event {
	return []abci.Event{{Type: "blob", Attributes: []abci.EventAttribute{{Key: "v", Value: strings.Repeat("x", n)}}}}
}

// TestResultCapNested: a msg dispatched from inside another (authz, a
// contract, ICA) is not metered itself; the outer msg, which re-emits its
// events, is metered once. Counting both would double-charge; counting only
// the inner would let a wrapper msg hide its own output.
func TestResultCapNested(t *testing.T) {
	var innerSaw, outerSaw bool
	inner := capResult(func(ctx sdk.Context, _ sdk.Msg) (*sdk.Result, error) {
		innerSaw = ctx.Value(nestedMsgKey{}) != nil
		return &sdk.Result{Events: eventsOfSize(600 << 10)}, nil
	})
	outer := capResult(func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		outerSaw = ctx.Value(nestedMsgKey{}) != nil
		res, err := inner(ctx, msg)
		if err != nil {
			return nil, err
		}
		return &sdk.Result{Events: append(res.Events, eventsOfSize(100)...)}, nil
	})

	m := &txResultMeter{}
	gas := storetypes.NewGasMeter(100_000_000)
	_, err := outer(capCtx(m, gas), &banktypes.MsgSend{})
	require.NoError(t, err)
	require.True(t, innerSaw)
	require.True(t, outerSaw)
	require.InDelta(t, 600<<10, m.bytes, 1024, "counted once")
	require.Equal(t, (m.bytes-resultFreeBytes)*resultGasPerByte, gas.GasConsumed())

	// A second msg in the same tx takes the total past the cap.
	_, err = outer(capCtx(m, gas), &banktypes.MsgSend{})
	require.ErrorIs(t, err, sdkerrors.ErrTxTooLarge)

	// Outside a tx (a gov proposal's msgs): the cap per msg, no gas.
	gas = storetypes.NewGasMeter(100_000_000)
	_, err = outer(capCtx(nil, gas), &banktypes.MsgSend{})
	require.NoError(t, err)
	require.Zero(t, gas.GasConsumed())
	big := capResult(func(sdk.Context, sdk.Msg) (*sdk.Result, error) {
		return &sdk.Result{Events: eventsOfSize(maxTxResultBytes)}, nil
	})
	_, err = big(capCtx(nil, gas), &banktypes.MsgSend{})
	require.ErrorIs(t, err, sdkerrors.ErrTxTooLarge)
}

func TestTruncateErrKeepsCode(t *testing.T) {
	short := wasmtypes.ErrExecuteFailed.Wrap("short")
	require.Same(t, short, truncateErr(short))

	long := wasmtypes.ErrExecuteFailed.Wrap(strings.Repeat("é", 4000))
	got := truncateErr(errorsmod.Wrapf(long, "failed to execute message; message index: %d", 0))
	space, code, log := errorsmod.ABCIInfo(got, false)
	require.Equal(t, wasmtypes.ModuleName, space)
	require.Equal(t, wasmtypes.ErrExecuteFailed.ABCICode(), code)
	require.LessOrEqual(t, len(log), maxErrorLogBytes+64)
	require.True(t, strings.HasSuffix(log, "bytes)"))
	require.True(t, errors.Is(got, wasmtypes.ErrExecuteFailed))
	require.NotContains(t, log, "�", "cut on a rune boundary")
}

// TestResultCapRoutesWrapped: every msg route of the built app goes through
// capResult. If an SDK bump renamed the router's map, New would panic; this
// pins that the wrapping also took effect on the routes baseapp uses.
func TestResultCapRoutesWrapped(t *testing.T) {
	e := initShieldedEnv(t)
	m := &txResultMeter{}
	ctx := e.ctx().WithValue(txResultMeterKey{}, m).WithGasMeter(storetypes.NewInfiniteGasMeter())
	h := e.app.MsgServiceRouter().Handler(&banktypes.MsgSend{})
	require.NotNil(t, h)
	to := e.bech(e.userAddr())
	_, err := h(ctx, &banktypes.MsgSend{FromAddress: to, ToAddress: to, Amount: e.fee(1)})
	require.NoError(t, err)
	require.NotZero(t, m.bytes, "the bank route is metered")

	// A second wrap is harmless (the inner layer sees the nested marker and
	// passes through); count the routes it covers.
	n, err := wrapMsgRoutes(e.app.MsgServiceRouter())
	require.NoError(t, err)
	require.Greater(t, n, 50)
}
