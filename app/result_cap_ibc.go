package app

import (
	"strconv"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	icatypes "github.com/cosmos/ibc-go/v10/modules/apps/27-interchain-accounts/types"
	ibccallbackstypes "github.com/cosmos/ibc-go/v10/modules/apps/callbacks/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	porttypes "github.com/cosmos/ibc-go/v10/modules/core/05-port/types"
	ibcapi "github.com/cosmos/ibc-go/v10/modules/core/api"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
)

// What one IBC packet's handling may put into a relay msg's result (round 8,
// R8-C-1). Relay txs have no byte cap of their own (resultcap), so that no
// packet is ever undeliverable for its size; what the packet's contents can
// make earth emit is bounded here instead, at the application.
//
// Every IBC application route (v1 and v2) is wrapped. The application runs on
// a branch of its context with a fresh event manager; then
//
//   - the error texts it put into its events (error attributes of ICS-20, the
//     ICA host and contract ports, callback_error of the callbacks
//     middleware) are cut to maxErrorLogBytes;
//   - on receive, if its events plus twice its acknowledgement (core logs it
//     hex-encoded) pass maxPacketAppResultBytes, its state and events are
//     dropped and the packet is acknowledged with an error: the sender's
//     chain refunds, and the relay msg stays small;
//   - on acknowledgement and timeout, a contract-owned port past the cap fails
//     the msg, as the contract returning an error would (its own channel);
//     ICS-20 and the ICA controller are never failed for size (their only
//     contract code, callbacks, is capped by cappedCallbacks, and the rest is
//     what the packet and acknowledgement in the relay tx already carry).
//
// Otherwise the branch is written back and its events emitted, unchanged but
// for the cut errors. Gas is untouched (the branch shares the gas meter), so
// all of this is a pure function of the packet and state: deterministic.

// errorAttrs are the (event type, attribute key) pairs that carry an error's
// text in what IBC applications emit.
var errorAttrs = map[[2]string]bool{
	{ibctransfertypes.EventTypePacket, ibctransfertypes.AttributeKeyAckError}:                     true,
	{icatypes.EventTypePacket, icatypes.AttributeKeyAckError}:                                     true,
	{wasmtypes.EventTypePacketRecv, wasmtypes.AttributeKeyAckError}:                               true,
	{ibccallbackstypes.EventTypeSourceCallback, ibccallbackstypes.AttributeKeyCallbackError}:      true,
	{ibccallbackstypes.EventTypeDestinationCallback, ibccallbackstypes.AttributeKeyCallbackError}: true,
}

// truncateErrorAttrs returns evs with every errorAttrs value cut to
// maxErrorLogBytes; events without one are kept as they are.
func truncateErrorAttrs(evs sdk.Events) sdk.Events {
	out := make(sdk.Events, len(evs))
	for i, ev := range evs {
		out[i] = ev
		copied := false
		for j, a := range ev.Attributes {
			if len(a.Value) <= maxErrorLogBytes || !errorAttrs[[2]string{ev.Type, a.Key}] {
				continue
			}
			if !copied {
				out[i].Attributes = append([]abci.EventAttribute(nil), ev.Attributes...)
				copied = true
			}
			out[i].Attributes[j].Value = truncateText(a.Value)
		}
	}
	return out
}

// runBounded runs an IBC application callback on a branch of ctx. run returns
// the bytes its result adds besides events (twice the acknowledgement) and
// its error. The branch is written back and its events, with their errors
// cut, emitted into ctx when run returns no error and, if enforce, the result
// is within maxPacketAppResultBytes. It returns the result's size and whether
// it was within the cap.
func runBounded(ctx sdk.Context, enforce bool, run func(sdk.Context) (uint64, error)) (uint64, bool, error) {
	cms := ctx.MultiStore().CacheMultiStore()
	branch := ctx.WithMultiStore(cms).WithEventManager(sdk.NewEventManager())
	extra, err := run(branch)
	if err != nil {
		return 0, true, truncateErr(err)
	}
	evs := truncateErrorAttrs(branch.EventManager().Events())
	n := eventBytes(evs.ToABCIEvents()) + extra
	if enforce && n > maxPacketAppResultBytes {
		return n, false, nil
	}
	cms.Write()
	ctx.EventManager().EmitEvents(evs)
	return n, true, nil
}

// eventTypePacketResultCapped marks a packet whose handling was dropped for
// passing maxPacketAppResultBytes (on receive core renames it, as every event
// of a failed receive, to ibccallbackerror-packet_result_capped).
const (
	eventTypePacketResultCapped = "packet_result_capped"
	attributeKeyResultBytes     = "result_bytes"
	attributeKeyResultLimit     = "result_limit"
)

func errPacketResultCapped(n uint64) error {
	return sdkerrors.ErrTxTooLarge.Wrapf("ibc application result is %d bytes, over the %d-byte limit", n, maxPacketAppResultBytes)
}

func emitPacketResultCapped(ctx sdk.Context, port, channelOrClient string, sequence, n uint64) {
	ctx.EventManager().EmitEvent(sdk.NewEvent(eventTypePacketResultCapped,
		sdk.NewAttribute("port", port),
		sdk.NewAttribute("channel", channelOrClient),
		sdk.NewAttribute("sequence", strconv.FormatUint(sequence, 10)),
		sdk.NewAttribute(attributeKeyResultBytes, strconv.FormatUint(n, 10)),
		sdk.NewAttribute(attributeKeyResultLimit, strconv.Itoa(maxPacketAppResultBytes)),
	))
}

// boundedIBCModule is an IBC v1 application route with its packet results
// bounded (see the top of this file). Only the router sees it: ICS4Wrapper
// wiring (WithICS4Wrapper) keeps the unwrapped stacks.
type boundedIBCModule struct {
	porttypes.IBCModule
	// contractPort: a contract-owned port, whose acknowledgement and timeout
	// handlers are capped too.
	contractPort bool
}

func (m boundedIBCModule) OnRecvPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, relayer sdk.AccAddress) ibcexported.Acknowledgement {
	var ack ibcexported.Acknowledgement
	n, ok, _ := runBounded(ctx, true, func(branch sdk.Context) (uint64, error) {
		ack = m.IBCModule.OnRecvPacket(branch, channelVersion, packet, relayer)
		if ack == nil {
			return 0, nil // asynchronous
		}
		return 2 * uint64(len(ack.Acknowledgement())), nil
	})
	if !ok {
		emitPacketResultCapped(ctx, packet.DestinationPort, packet.DestinationChannel, packet.Sequence, n)
		return channeltypes.NewErrorAcknowledgement(errPacketResultCapped(n))
	}
	return ack
}

func (m boundedIBCModule) OnAcknowledgementPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, acknowledgement []byte, relayer sdk.AccAddress) error {
	n, ok, err := runBounded(ctx, m.contractPort, func(branch sdk.Context) (uint64, error) {
		return 0, m.IBCModule.OnAcknowledgementPacket(branch, channelVersion, packet, acknowledgement, relayer)
	})
	if err != nil {
		return err
	}
	if !ok {
		return errPacketResultCapped(n)
	}
	return nil
}

func (m boundedIBCModule) OnTimeoutPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, relayer sdk.AccAddress) error {
	n, ok, err := runBounded(ctx, m.contractPort, func(branch sdk.Context) (uint64, error) {
		return 0, m.IBCModule.OnTimeoutPacket(branch, channelVersion, packet, relayer)
	})
	if err != nil {
		return err
	}
	if !ok {
		return errPacketResultCapped(n)
	}
	return nil
}

// boundedIBCModuleV2 is boundedIBCModule for an IBC v2 application route.
type boundedIBCModuleV2 struct {
	ibcapi.IBCModule
	contractPort bool
}

func (m boundedIBCModuleV2) OnRecvPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64,
	payload channeltypesv2.Payload, relayer sdk.AccAddress,
) channeltypesv2.RecvPacketResult {
	var res channeltypesv2.RecvPacketResult
	n, ok, _ := runBounded(ctx, true, func(branch sdk.Context) (uint64, error) {
		res = m.IBCModule.OnRecvPacket(branch, sourceClient, destinationClient, sequence, payload, relayer)
		return 2 * uint64(len(res.Acknowledgement)), nil
	})
	if !ok {
		emitPacketResultCapped(ctx, payload.DestinationPort, destinationClient, sequence, n)
		return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Failure}
	}
	return res
}

func (m boundedIBCModuleV2) OnAcknowledgementPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64,
	acknowledgement []byte, payload channeltypesv2.Payload, relayer sdk.AccAddress,
) error {
	n, ok, err := runBounded(ctx, m.contractPort, func(branch sdk.Context) (uint64, error) {
		return 0, m.IBCModule.OnAcknowledgementPacket(branch, sourceClient, destinationClient, sequence, acknowledgement, payload, relayer)
	})
	if err != nil {
		return err
	}
	if !ok {
		return errPacketResultCapped(n)
	}
	return nil
}

func (m boundedIBCModuleV2) OnTimeoutPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64,
	payload channeltypesv2.Payload, relayer sdk.AccAddress,
) error {
	n, ok, err := runBounded(ctx, m.contractPort, func(branch sdk.Context) (uint64, error) {
		return 0, m.IBCModule.OnTimeoutPacket(branch, sourceClient, destinationClient, sequence, payload, relayer)
	})
	if err != nil {
		return err
	}
	if !ok {
		return errPacketResultCapped(n)
	}
	return nil
}
