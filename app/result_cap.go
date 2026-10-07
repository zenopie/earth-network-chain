package app

import (
	"context"
	"fmt"
	"reflect"
	"unicode/utf8"
	"unsafe"

	errorsmod "cosmossdk.io/errors"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	ibccallbackstypes "github.com/cosmos/ibc-go/v10/modules/apps/callbacks/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	"github.com/earth-network/earth/app/resultcap"
)

// The bytes a transaction, a gov EndBlock or an IBC packet handler may leave
// in the chain's ABCI results. The rules, the numbers and the worst cases
// they give are documented in package resultcap, which holds the limits as
// exported constants (read by tools outside the chain); this file enforces
// them:
//
//   - capResult, around every msg handler: the gas per byte past the free
//     tier, the per-msg and per-tx caps of non-relay txs, gov's per-EndBlock
//     total, and error truncation for top-level and nested msgs alike;
//   - boundedIBCModule / boundedIBCModuleV2, around every IBC application
//     route: the cap on what one packet's handling may emit, and error
//     truncation in the applications' error attributes;
//   - cappedCallbacks, around the IBC callbacks' contract keeper: the cap on
//     one callback's events, and its error truncated.

const (
	maxMsgResultBytes       = resultcap.MaxMsgResultBytes
	maxTxResultBytes        = resultcap.MaxTxResultBytes
	maxEndBlockResultBytes  = resultcap.MaxEndBlockResultBytes
	maxPacketAppResultBytes = resultcap.MaxPacketAppResultBytes
	maxCallbackResultBytes  = resultcap.MaxCallbackResultBytes
	maxErrorLogBytes        = resultcap.MaxErrorBytes
	resultFreeBytes         = resultcap.FreeBytes
	resultGasPerByte        = resultcap.GasPerByte
	resultEventOverhead     = resultcap.EventOverhead
	resultAttributeOverhead = resultcap.AttributeOverhead
	resultMsgOverhead       = resultcap.MsgOverhead
)

// resultMeter is a running total, installed by the ante wrapper for a tx (and
// read by every top-level msg handler of the same tx) or by the gov module
// wrapper for an EndBlock.
type resultMeter struct {
	endBlock  bool   // gov proposals in EndBlock: a total cap, no gas
	relayOnly bool   // a tx of relay msgs only: gas, no byte cap
	bytes     uint64 // msg-phase result bytes so far
	charged   uint64 // gas already charged for them (tx only)
}

type resultMeterKey struct{}

// nestedMsgKey marks a context as being inside a msg handler: a msg reached
// from it (authz MsgExec, a contract's dispatched msg, an ICA host tx) is
// nested, and its events and response are already counted in the outer msg's
// result, which re-emits them.
type nestedMsgKey struct{}

// withResultMeter wraps the ante handler so that every tx carries a fresh
// meter into its msgs. The value travels in the context the ante returns,
// which baseapp branches for runMsgs.
func withResultMeter(next sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return next(ctx.WithValue(resultMeterKey{}, &resultMeter{relayOnly: isRelayTx(tx.GetMsgs())}), tx, simulate)
	}
}

// isRelayTx: a tx of msgs is a relay tx, every msg a relay msg
// (resultcap.RelayMsgTypeURLs).
func isRelayTx(msgs []sdk.Msg) bool {
	if len(msgs) == 0 {
		return false
	}
	for _, msg := range msgs {
		if !resultcap.IsRelayMsg(sdk.MsgTypeURL(msg)) {
			return false
		}
	}
	return true
}

// withEndBlockMeter gives an EndBlock (gov's) a fresh per-block meter.
func withEndBlockMeter(ctx sdk.Context) sdk.Context {
	return ctx.WithValue(resultMeterKey{}, &resultMeter{endBlock: true})
}

// jsonExtra is, for each ASCII byte, how many bytes its JSON rendering adds
// in the answers a node gives for results (resultcap, "How bytes are
// counted"): Go's encoding/json, under CometBFT's RPC and gogoproto jsonpb
// (the SDK's LCD) alike, writes < > & and the control characters other than
// \b \f \n \r \t as the 6-byte \u00XX, and " \ and those five as 2 bytes.
// protojson escapes a subset of these, no more.
var jsonExtra = func() (t [utf8.RuneSelf]uint8) {
	for c := 0; c < 0x20; c++ {
		t[c] = resultcap.JSONEscapeBytes - 1
	}
	for _, c := range "<>&" {
		t[c] = resultcap.JSONEscapeBytes - 1
	}
	for _, c := range "\"\\\b\f\n\r\t" {
		t[c] = 1
	}
	return t
}()

// jsonLen is the length of s as a JSON string's contents in the node's
// answers, at its worst across the encoders (resultcap, "How bytes are
// counted"): ASCII per jsonExtra; an invalid UTF-8 byte is written as
// \ufffd (6 bytes); U+2028 and U+2029 (3 bytes) as \u2028 and \u2029 (6);
// every other rune as itself. One pass over s, no allocation; an ASCII
// string free of escapes is len(s).
func jsonLen(s string) uint64 {
	n := uint64(len(s))
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			n += uint64(jsonExtra[c])
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += resultcap.JSONEscapeBytes - 1
		case r == '\u2028' || r == '\u2029':
			n += resultcap.JSONEscapeBytes - 3
		}
		i += size
	}
	return n
}

// eventBytes is what events add to a stored result, each string counted at
// its JSON length (jsonLen): the proto size of the events, plus the
// overheads, plus what escaping adds to their types, keys and values.
func eventBytes(events []abci.Event) uint64 {
	var n uint64
	for _, e := range events {
		n += uint64(e.Size()) + resultEventOverhead + resultAttributeOverhead*uint64(len(e.Attributes))
		n += jsonLen(e.Type) - uint64(len(e.Type))
		for _, a := range e.Attributes {
			n += jsonLen(a.Key) - uint64(len(a.Key)) + jsonLen(a.Value) - uint64(len(a.Value))
		}
	}
	return n
}

// resultBytes is what a msg's result adds to the stored ExecTxResult.
func resultBytes(res *sdk.Result) uint64 {
	if res == nil {
		return 0
	}
	n := eventBytes(res.Events)
	for _, r := range res.MsgResponses {
		if r != nil {
			n += resultcap.ResponseByteWeight*uint64(r.Size()) + resultEventOverhead
		}
	}
	// res.Data is the same response marshalled again (WrapServiceResult);
	// baseapp stores only MsgResponses, in ExecTxResult.Data, which answers
	// render as bytes: base64 (RPC) or hex (LCD tx_response.data), hence
	// ResponseByteWeight. It adds the msg's "message" event (action, sender,
	// module), counted as a flat resultMsgOverhead.
	return n + resultMsgOverhead
}

// capResult wraps one msg handler.
func capResult(h baseapp.MsgServiceHandler) baseapp.MsgServiceHandler {
	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		if ctx.Value(nestedMsgKey{}) != nil {
			// Counted in the top-level msg. Its error is cut here too: an
			// ICA host, authz or a contract's submessage may put it into an
			// event of the outer msg's result.
			res, err := h(ctx, msg)
			if err != nil {
				return nil, truncateErr(err)
			}
			return res, nil
		}
		res, err := h(ctx.WithValue(nestedMsgKey{}, true), msg)
		if err != nil {
			return nil, truncateErr(err)
		}
		n := resultBytes(res)
		m, _ := ctx.Value(resultMeterKey{}).(*resultMeter)
		inRelayTx := m != nil && !m.endBlock && m.relayOnly
		if n > maxMsgResultBytes && !inRelayTx {
			return nil, sdkerrors.ErrTxTooLarge.Wrapf("msg result is %d bytes, over the %d-byte limit", n, maxMsgResultBytes)
		}
		switch {
		case m == nil:
			// Neither a tx nor a metered EndBlock: the per-msg cap alone.
			return res, nil
		case m.endBlock:
			m.bytes += n
			if m.bytes > maxEndBlockResultBytes {
				return nil, sdkerrors.ErrTxTooLarge.Wrapf("proposal msgs of this block have %d result bytes, over the %d-byte limit", m.bytes, maxEndBlockResultBytes)
			}
			return res, nil
		}
		m.bytes += n
		if !m.relayOnly && m.bytes > maxTxResultBytes {
			return nil, sdkerrors.ErrTxTooLarge.Wrapf("tx msg results are %d bytes, over the %d-byte limit", m.bytes, maxTxResultBytes)
		}
		if m.bytes > resultFreeBytes {
			owed := (m.bytes - resultFreeBytes) * resultGasPerByte
			ctx.GasMeter().ConsumeGas(owed-m.charged, "tx result bytes")
			m.charged = owed
		}
		return res, nil
	}
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
	if err == nil {
		return nil
	}
	s := err.Error()
	if jsonLen(s) <= maxErrorLogBytes {
		return err
	}
	return &truncatedErr{msg: truncateText(s), cause: err}
}

// truncateText cuts an error text to at most maxErrorLogBytes of JSON
// (jsonLen), on a rune boundary, and says how long it was. A text within the
// limit is returned as it is.
func truncateText(s string) string {
	var n uint64
	for i := 0; i < len(s); {
		size := 1
		if s[i] >= utf8.RuneSelf {
			_, size = utf8.DecodeRuneInString(s[i:])
		}
		n += jsonLen(s[i : i+size])
		if n > maxErrorLogBytes {
			return fmt.Sprintf("%s... (error truncated: %d bytes)", s[:i], len(s))
		}
		i += size
	}
	return s
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

// meteredGovModule is x/gov with a per-EndBlock result meter: every msg of
// every proposal that passes in this block shares it (see the top of this
// file). Everything else is gov.AppModule's own, promoted.
type meteredGovModule struct{ gov.AppModule }

func (m meteredGovModule) EndBlock(ctx context.Context) error {
	return m.AppModule.EndBlock(withEndBlockMeter(sdk.UnwrapSDKContext(ctx)))
}

// meterGovEndBlock swaps x/gov in the module manager for meteredGovModule.
// Must run before Load, which fixes the EndBlock order over these modules.
func meterGovEndBlock(mm *module.Manager) error {
	g, ok := mm.Modules[govtypes.ModuleName].(gov.AppModule)
	if !ok {
		return errorsmod.Wrapf(sdkerrors.ErrLogic, "module manager's %s is %T, not gov.AppModule", govtypes.ModuleName, mm.Modules[govtypes.ModuleName])
	}
	mm.Modules[govtypes.ModuleName] = meteredGovModule{g}
	return nil
}

// cappedCallbacks bounds what an IBC callback contract may emit
// (maxCallbackResultBytes) and the error text it may return
// (maxErrorLogBytes). The callbacks middleware runs each callback in a
// cache context of its own (fresh event manager) and writes it back only when
// the callback returns nil, so an over-cap callback's events and state are
// dropped; on receive the middleware then writes an error acknowledgement.
type cappedCallbacks struct {
	ibccallbackstypes.ContractKeeper
}

func capCallback(ctx sdk.Context, run func() error) error {
	before := len(ctx.EventManager().Events())
	if err := run(); err != nil {
		// The middleware writes it into the callback_error attribute, and a
		// contract chooses its own error text (R8-C-1).
		return truncateErr(err)
	}
	if n := eventBytes(ctx.EventManager().Events()[before:].ToABCIEvents()); n > maxCallbackResultBytes {
		return sdkerrors.ErrTxTooLarge.Wrapf("ibc callback emitted %d bytes of events, over the %d-byte limit", n, maxCallbackResultBytes)
	}
	return nil
}

func (c cappedCallbacks) IBCSendPacketCallback(ctx sdk.Context, sourcePort, sourceChannel string, timeoutHeight clienttypes.Height,
	timeoutTimestamp uint64, packetData []byte, contractAddress, packetSenderAddress, version string,
) error {
	return capCallback(ctx, func() error {
		return c.ContractKeeper.IBCSendPacketCallback(ctx, sourcePort, sourceChannel, timeoutHeight, timeoutTimestamp, packetData,
			contractAddress, packetSenderAddress, version)
	})
}

func (c cappedCallbacks) IBCOnAcknowledgementPacketCallback(ctx sdk.Context, packet channeltypes.Packet, acknowledgement []byte,
	relayer sdk.AccAddress, contractAddress, packetSenderAddress, version string,
) error {
	return capCallback(ctx, func() error {
		return c.ContractKeeper.IBCOnAcknowledgementPacketCallback(ctx, packet, acknowledgement, relayer, contractAddress, packetSenderAddress, version)
	})
}

func (c cappedCallbacks) IBCOnTimeoutPacketCallback(ctx sdk.Context, packet channeltypes.Packet, relayer sdk.AccAddress,
	contractAddress, packetSenderAddress, version string,
) error {
	return capCallback(ctx, func() error {
		return c.ContractKeeper.IBCOnTimeoutPacketCallback(ctx, packet, relayer, contractAddress, packetSenderAddress, version)
	})
}

func (c cappedCallbacks) IBCReceivePacketCallback(ctx sdk.Context, packet ibcexported.PacketI, ack ibcexported.Acknowledgement,
	contractAddress, version string,
) error {
	return capCallback(ctx, func() error {
		return c.ContractKeeper.IBCReceivePacketCallback(ctx, packet, ack, contractAddress, version)
	})
}
