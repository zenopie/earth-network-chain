package app

import (
	"fmt"
	"reflect"
	"unicode/utf8"
	"unsafe"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// The bytes a transaction may leave in its ABCI result (round-6 R6-E-1).
//
// A tx's ExecTxResult (events with their attributes, the msg responses in
// Data, the log) is stored twice by every node (the ABCI responses and the tx
// index) and kept for ever, and every block_results / tx / tx-by-hash answer
// rebuilds it in memory at roughly six times its stored size. Nothing in the
// SDK bounds it: block max_bytes bounds tx bytes, not results, and wasmd
// charges about one gas per byte of contract events and nothing per byte of
// response data. One ~100M-gas tx of three contract executes could store
// ~100 MB, and a handful of concurrent reads of that height OOM the node, at
// every later read too, because the height is permanent.
//
// So the msg phase of every tx is metered here, in consensus:
//
//   - The first resultFreeBytes of a tx are free.
//   - Each byte past that costs resultGasPerByte gas on the tx's gas meter, so
//     block gas bounds what a block of results can hold.
//   - Past maxTxResultBytes the tx fails with ErrTxTooLarge: its msgs' state
//     is reverted, its fee is charged, and what is stored is the ante's events
//     and a one-line log.
//
// A failed msg's error is the log a failed tx stores, and a contract chooses
// its own error string (up to its 32 MiB of memory), so an error longer than
// maxErrorLogBytes is cut to that before it leaves the router. Its ABCI code
// and codespace are kept.
//
// What a block can hold then: the paid bytes are at most max_gas /
// resultGasPerByte = 5 MB at max_gas 100M; the free tiers at most one per tx,
// and a tx that turns little gas into 8 KiB of output needs a contract
// (wasmd's 60k instance cost plus ~30k of ante), so ~1,100 txs and ~9 MB. The
// two share block gas, so ~10 MB is the worst case, against ~100 MB before.
// Native msgs only echo their input, which already costs 10 gas per tx byte.
//
// The ante phase is not metered. A signed tx's ante events are a fixed
// handful (fee, signatures, sequence), under 1 KB. A private tx runs its
// whole msg in the ante, so its notes and nullifiers are ante events: a
// function of the tx's shape (6.5 KB at most in the chain's tests), paid by
// its flat private gas and capped per block by max_private_actions_per_block.
//
// Sizes measured over every flow in app's tests (largest msg-phase result per
// tx): MsgRegister 5.4 KB, MsgBuyAnml 2.6 KB, MsgClaimAnml 2.0 KB, shielded
// staking 1-2 KB, gov 1.6 KB, bank 0.8 KB. An ICS-20 MsgRecvPacket is ~4 KB
// (packet data hex-encoded twice), ~170 KB with ibc-go's 32 KiB memo maximum.
//
// The numbers are consensus: changing one is a state-machine change.
const (
	// maxTxResultBytes caps a tx's msg-phase result (events and responses of
	// all its msgs together): far above any legitimate tx, including a relay
	// batch of dozens of packets or one packet with the largest memo, and
	// small enough that a tx-by-hash answer cannot hurt the node.
	maxTxResultBytes = 1 << 20 // 1 MiB

	// resultFreeBytes is the per-tx allowance that costs no extra gas: 1.5x
	// the largest single msg of the chain's own flows (MsgRegister), so no
	// wallet's or gas-check's gas changes.
	resultFreeBytes = 8 << 10 // 8 KiB

	// resultGasPerByte prices the bytes past the free allowance: twice the
	// tx-bytes price (auth TxSizeCostPerByte 10), as a result is stored twice.
	resultGasPerByte = 20

	// maxErrorLogBytes caps the error text a failed msg returns. Errors are a
	// line or two; a cheap failing contract call should not buy kilobytes.
	maxErrorLogBytes = 1 << 10 // 1 KiB

	// Overheads counted on top of the proto size of what a msg returns:
	// baseapp appends a msg_index attribute to every event, MarkEventsToIndex
	// sets every attribute's index flag, and each msg gets a "message" event
	// (action, sender, module). Generous, so that many tiny events are not
	// cheaper than a few large ones.
	resultEventOverhead     = 24
	resultAttributeOverhead = 4
	resultMsgOverhead       = 256
)

// txResultMeter is a tx's running total, installed by the ante wrapper and
// read by every top-level msg handler of the same tx.
type txResultMeter struct {
	bytes   uint64 // msg-phase result bytes so far
	charged uint64 // gas already charged for them
}

type txResultMeterKey struct{}

// nestedMsgKey marks a context as being inside a msg handler: a msg reached
// from it (authz MsgExec, a contract's dispatched msg, an ICA host tx, a gov
// proposal's msgs) is nested, and its events and response are already counted
// in the outer msg's result, which re-emits them.
type nestedMsgKey struct{}

// withResultMeter wraps the ante handler so that every tx carries a fresh
// meter into its msgs. The value travels in the context the ante returns,
// which baseapp branches for runMsgs.
func withResultMeter(next sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return next(ctx.WithValue(txResultMeterKey{}, &txResultMeter{}), tx, simulate)
	}
}

// resultBytes is what a msg's result adds to the stored ExecTxResult.
func resultBytes(res *sdk.Result) uint64 {
	if res == nil {
		return 0
	}
	var n uint64
	for _, e := range res.Events {
		n += uint64(e.Size()) + resultEventOverhead + resultAttributeOverhead*uint64(len(e.Attributes))
	}
	for _, r := range res.MsgResponses {
		if r != nil {
			n += uint64(r.Size()) + resultEventOverhead
		}
	}
	// res.Data is the same response marshalled again (WrapServiceResult);
	// baseapp stores only MsgResponses. It adds the msg's "message" event
	// (action, sender, module), counted as a flat resultMsgOverhead.
	return n + resultMsgOverhead
}

// capResult wraps one msg handler.
func capResult(h baseapp.MsgServiceHandler) baseapp.MsgServiceHandler {
	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		if ctx.Value(nestedMsgKey{}) != nil {
			return h(ctx, msg)
		}
		res, err := h(ctx.WithValue(nestedMsgKey{}, true), msg)
		if err != nil {
			return nil, truncateErr(err)
		}
		n := resultBytes(res)
		m, _ := ctx.Value(txResultMeterKey{}).(*txResultMeter)
		if m == nil {
			// Not in a tx (a gov proposal's msgs in EndBlock): the cap alone,
			// per msg. There is no tx gas meter to charge.
			if n > maxTxResultBytes {
				return nil, errTooLarge(n)
			}
			return res, nil
		}
		m.bytes += n
		if m.bytes > maxTxResultBytes {
			return nil, errTooLarge(m.bytes)
		}
		if m.bytes > resultFreeBytes {
			owed := (m.bytes - resultFreeBytes) * resultGasPerByte
			ctx.GasMeter().ConsumeGas(owed-m.charged, "tx result bytes")
			m.charged = owed
		}
		return res, nil
	}
}

func errTooLarge(n uint64) error {
	return sdkerrors.ErrTxTooLarge.Wrapf("tx result is %d bytes, over the %d-byte limit", n, maxTxResultBytes)
}

// truncatedErr keeps the ABCI code and codespace of the error it cuts short
// (errorsmod finds them through Cause) and replaces only its text.
type truncatedErr struct {
	msg   string
	cause error
}

func (e *truncatedErr) Error() string { return e.msg }
func (e *truncatedErr) Cause() error  { return e.cause }
func (e *truncatedErr) Unwrap() error { return e.cause }

func truncateErr(err error) error {
	s := err.Error()
	if len(s) <= maxErrorLogBytes {
		return err
	}
	cut := maxErrorLogBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return &truncatedErr{msg: fmt.Sprintf("%s... (error truncated: %d bytes)", s[:cut], len(s)), cause: err}
}

// wrapMsgRoutes puts capResult around every handler in baseapp's msg router.
//
// SDK v0.53 has no hook that sees a msg's result inside the tx: the post
// handler gets neither the events nor the responses, the circuit breaker
// runs before the handler, and MsgServiceRouter's route map is unexported.
// Wrapping its entries in place is the one point every msg passes, top-level
// (baseapp.runMsgs) and nested (authz, gov, wasm, ICA, all through this
// router or OperatorRewardsRouter over it) alike, without forking baseapp.
// The field is checked by name and type and New panics if it moves, so an SDK
// bump cannot drop the cap silently; TestResultCapRoutesWrapped also asserts
// it end to end.
//
// Must run after every module has registered its msg services (Build and
// registerIBCModules) and before the first tx.
func wrapMsgRoutes(msr *baseapp.MsgServiceRouter) (int, error) {
	f := reflect.ValueOf(msr).Elem().FieldByName("routes")
	want := reflect.TypeOf(map[string]baseapp.MsgServiceHandler(nil))
	if !f.IsValid() || f.Type() != want {
		return 0, errorsmod.Wrap(sdkerrors.ErrLogic, "baseapp.MsgServiceRouter has no routes map of the expected type")
	}
	routes := *(*map[string]baseapp.MsgServiceHandler)(unsafe.Pointer(f.UnsafeAddr()))
	for k, h := range routes {
		routes[k] = capResult(h)
	}
	return len(routes), nil
}
