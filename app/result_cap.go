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
)

// The bytes a transaction may leave in its ABCI result (round-6 R6-E-1,
// round-7 R7-C-1/R7-C-2).
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
//   - The first resultFreeBytes of a tx are free (per tx, not per msg).
//   - Each byte past that costs resultGasPerByte gas on the tx's gas meter, so
//     the tx's gas limit bounds its total and block gas bounds the block's.
//   - One top-level msg whose own result passes maxMsgResultBytes fails the
//     tx with ErrTxTooLarge: its msgs' state is reverted, its fee is charged,
//     and what is stored is the ante's events and a one-line log. The cap is
//     per msg, not per tx, so a relayer's batch of honest packets is never
//     failed by a few large ones beside them (R7-C-1); only the gas grows.
//
// Why the free tier is per tx: per msg, a tx of N cheap msgs would get N free
// tiers, and the block bound below would grow with the msg count. Per tx, a
// relay batch of 30 ordinary ICS-20 packets (3.7 KB each) pays ~2.1M gas for
// its ~110 KB, and each 32 KiB-memo packet (168 KB) ~3.4M more: a batch of
// 38 with eight of those stores 1.44 MB for 34M gas, inside the 100M block
// (TestResultCapRelayBatch). Relayers should still keep batches modest
// (docs: about 10 msgs) so one tx does not need most of a block's gas.
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
// One tx is at most ~5 MB (all of a block's gas). Native msgs only echo
// their input, which already costs 10 gas per tx byte.
//
// IBC callbacks (a contract named in a packet's memo, run inside the relayer's
// MsgRecvPacket / MsgAcknowledgement / MsgTimeout) may emit at most
// maxCallbackResultBytes of events; past that the callback fails, which on
// receive writes an error acknowledgement (the packet is received, the tokens
// refunded on the sender's chain) and on ack/timeout drops only the callback.
// With ibc-go's 32 KiB memo maximum a MsgRecvPacket's result is then at most
// ~170 KB + 256 KiB, under maxMsgResultBytes: no ICS-20 packet can be one that
// no relayer can ever deliver.
//
// Gov proposals run their msgs in EndBlock, outside any tx and with no gas.
// They share one meter per EndBlock: past maxEndBlockResultBytes in total the
// msg errors and its proposal fails (gov marks it FAILED and reverts it), so
// k proposals with k large msgs cannot put k MiB into one height (R7-C-2).
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
// (packet data hex-encoded twice), ~170 KB with ibc-go's 32 KiB memo maximum
// (TestResultCapRelayBatch measures both).
//
// The numbers are consensus: changing one is a state-machine change.
const (
	// maxMsgResultBytes caps one top-level msg's result (its events, nested
	// msgs' included, and its response): far above any legitimate msg,
	// including a packet with the largest memo, and small enough that the
	// msg's share of a tx-by-hash answer cannot hurt the node.
	maxMsgResultBytes = 1 << 20 // 1 MiB

	// maxEndBlockResultBytes caps the results of every gov-proposal msg run in
	// one EndBlock together.
	maxEndBlockResultBytes = 1 << 20 // 1 MiB

	// maxCallbackResultBytes caps the events of one IBC callback.
	maxCallbackResultBytes = 256 << 10 // 256 KiB

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

// resultMeter is a running total, installed by the ante wrapper for a tx (and
// read by every top-level msg handler of the same tx) or by the gov module
// wrapper for an EndBlock.
type resultMeter struct {
	endBlock bool   // gov proposals in EndBlock: a total cap, no gas
	bytes    uint64 // msg-phase result bytes so far
	charged  uint64 // gas already charged for them (tx only)
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
		return next(ctx.WithValue(resultMeterKey{}, &resultMeter{}), tx, simulate)
	}
}

// withEndBlockMeter gives an EndBlock (gov's) a fresh per-block meter.
func withEndBlockMeter(ctx sdk.Context) sdk.Context {
	return ctx.WithValue(resultMeterKey{}, &resultMeter{endBlock: true})
}

// eventBytes is what events add to a stored result.
func eventBytes(events []abci.Event) uint64 {
	var n uint64
	for _, e := range events {
		n += uint64(e.Size()) + resultEventOverhead + resultAttributeOverhead*uint64(len(e.Attributes))
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
		if n > maxMsgResultBytes {
			return nil, sdkerrors.ErrTxTooLarge.Wrapf("msg result is %d bytes, over the %d-byte limit", n, maxMsgResultBytes)
		}
		m, _ := ctx.Value(resultMeterKey{}).(*resultMeter)
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
// (maxCallbackResultBytes). The callbacks middleware runs each callback in a
// cache context of its own (fresh event manager) and writes it back only when
// the callback returns nil, so an over-cap callback's events and state are
// dropped; on receive the middleware then writes an error acknowledgement.
type cappedCallbacks struct {
	ibccallbackstypes.ContractKeeper
}

func capCallback(ctx sdk.Context, run func() error) error {
	before := len(ctx.EventManager().Events())
	if err := run(); err != nil {
		return err
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
