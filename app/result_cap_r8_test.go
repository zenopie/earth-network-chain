package app

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	coretypes2 "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/gogoproto/proto"
	icacontrollertypes "github.com/cosmos/ibc-go/v10/modules/apps/27-interchain-accounts/controller/types"
	icatypes "github.com/cosmos/ibc-go/v10/modules/apps/27-interchain-accounts/types"
	ibccallbackstypes "github.com/cosmos/ibc-go/v10/modules/apps/callbacks/types"
	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	porttypes "github.com/cosmos/ibc-go/v10/modules/core/05-port/types"
	ibcapi "github.com/cosmos/ibc-go/v10/modules/core/api"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	coretypes "github.com/cosmos/ibc-go/v10/modules/core/types"
	localhost "github.com/cosmos/ibc-go/v10/modules/light-clients/09-localhost"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/app/resultcap"
)

// Round 8 (R8-C-1, R8-D-1): relay txs have no byte cap, only gas; what a
// packet can make earth emit is bounded at the application instead; ordinary
// txs have a 1 MiB total again.

// callbackMemo is an ICS-20 memo naming addr as the destination callback,
// padded to size bytes.
func callbackMemo(t *testing.T, addr string, size int) string {
	bz, err := json.Marshal(map[string]any{"dest_callback": map[string]any{"address": addr}})
	require.NoError(t, err)
	if size == 0 {
		return string(bz)
	}
	head := strings.TrimSuffix(string(bz), "}") + `,"pad":"`
	pad := size - len(head) - len(`"}`)
	require.Positive(t, pad)
	memo := head + strings.Repeat("p", pad) + `"}`
	require.Len(t, memo, size)
	return memo
}

func ackOf(t *testing.T, ev map[string]string) string {
	bz, err := hex.DecodeString(ev[channeltypes.AttributeKeyAckHex])
	require.NoError(t, err)
	return string(bz)
}

// errAttr is an error event's error attribute, as core renames both for a
// failed receive.
func errAttr(evs []abci.Event, typ, key string) []string {
	var out []string
	for _, ev := range eventsOf(evs, coretypes.ErrorAttributeKeyPrefix+typ) {
		out = append(out, ev[coretypes.ErrorAttributeKeyPrefix+key])
	}
	return out
}

// TestResultCapUndeliverablePacket is R8-C-1's attack: an ICS-20 transfer
// with the largest memo, whose destination callback fails with a ~0.9 MB
// error. At 0a292ae the error went whole into callback_error, the
// MsgRecvPacket's result passed the per-msg cap, and no relayer could ever
// deliver the packet (code 21), nor any batch holding it. Now the error is
// cut to 1 KiB and the packet is delivered with an error acknowledgement in
// the same batch as honest ones, which succeed.
func TestResultCapUndeliverablePacket(t *testing.T) {
	results := func(t *testing.T) []byte {
		e := newCapEnv(t, "result-cap-r8-attack")
		e.openTransferChannel()
		errCB := e.deploy("cb-error", callbackContract(errPrefix, 895_000, errSuffix))
		receiver := e.bech(sdk.AccAddress([]byte("ibc-receiver-account")))
		relayerKey := secp256k1.GenPrivKeyFromSecret([]byte("result-cap-r8-attack/validator"))
		relayer := e.bech(sdk.AccAddress(relayerKey.PubKey().Address()))

		packets := e.transfer(receiver, "", callbackMemo(t, errCB, transfertypes.MaximumMemoLength), "")
		const gas = 30_000_000
		fb := e.finalize(e.signedTxAs(relayerKey, gas, e.gasFee(gas), e.recvMsgs(relayer, packets)...))
		r := fb.TxResults[0]
		requireOK(t, r)

		acks := eventsOf(r.Events, channeltypes.EventTypeWriteAck)
		require.Len(t, acks, 3, "every packet received, the attacker's too")
		require.Contains(t, ackOf(t, acks[0]), `"result"`)
		require.Contains(t, ackOf(t, acks[1]), `"error"`, "the failed callback: an error acknowledgement")
		require.Contains(t, ackOf(t, acks[2]), `"result"`)

		cbErr := errAttr(r.Events, ibccallbackstypes.EventTypeDestinationCallback, ibccallbackstypes.AttributeKeyCallbackError)
		require.Len(t, cbErr, 1)
		require.Contains(t, cbErr[0], "error truncated: ")
		require.LessOrEqual(t, len(cbErr[0]), resultcap.MaxErrorBytes+64)

		size := resultSize(t, r)
		t.Logf("relay tx with the attack packet: %d result bytes, %d gas", size, r.GasUsed)
		require.Less(t, size, 256<<10, "the ~0.9 MB error is not stored")
		for idx, n := range msgBytes(r.Events) {
			t.Logf("msg %s: %d bytes", idx, n)
		}
		bal := e.app.BankKeeper.GetAllBalances(e.ctx(), sdk.MustAccAddressFromBech32(receiver))
		require.Len(t, bal, 1)
		require.Equal(t, int64(2), bal[0].Amount.Int64(), "the honest packets' vouchers minted, not the attacker's")
		bz, err := r.Marshal()
		require.NoError(t, err)
		return bz
	}
	a := results(t)
	require.Equal(t, a, results(t), "deterministic: the same result bytes on a second chain")
}

// --- ICA host over the localhost connection ----------------------------------

// openICAChannel registers an interchain account owned by the user on
// earth's own ICA host (controller and host both over 09-localhost) and
// returns the controller port, its channel and the account's address.
func (e *capEnv) openICAChannel() (string, string, string) {
	e.t.Helper()
	t := e.t
	owner := e.bech(e.userAddr())
	conn := ibcexported.LocalhostConnectionID
	fb := e.finalize(e.signedTx(2_000_000, e.fee(20_000),
		icacontrollertypes.NewMsgRegisterInterchainAccount(conn, owner, "", channeltypes.UNORDERED)))
	requireOK(t, fb.TxResults[0])
	inits := eventsOf(fb.TxResults[0].Events, channeltypes.EventTypeChannelOpenInit)
	require.Len(t, inits, 1)
	ctrlPort, ctrlChan := inits[0][channeltypes.AttributeKeyPortID], inits[0][channeltypes.AttributeKeyChannelID]
	ch, ok := e.app.IBCKeeper.ChannelKeeper.GetChannel(e.ctx(), ctrlPort, ctrlChan)
	require.True(t, ok)

	signer := owner
	proof := localhost.SentinelProof
	hops := []string{conn}
	h := clienttypes.GetSelfHeight(e.ctx())
	fb = e.finalize(e.signedTx(2_000_000, e.fee(20_000), channeltypes.NewMsgChannelOpenTry(
		icatypes.HostPortID, ch.Version, channeltypes.UNORDERED, hops, ctrlPort, ctrlChan, ch.Version, proof, h, signer)))
	requireOK(t, fb.TxResults[0])
	tries := eventsOf(fb.TxResults[0].Events, channeltypes.EventTypeChannelOpenTry)
	require.Len(t, tries, 1)
	hostChan := tries[0][channeltypes.AttributeKeyChannelID]
	hostCh, ok := e.app.IBCKeeper.ChannelKeeper.GetChannel(e.ctx(), icatypes.HostPortID, hostChan)
	require.True(t, ok)

	h = clienttypes.GetSelfHeight(e.ctx())
	fb = e.finalize(e.signedTx(2_000_000, e.fee(20_000),
		channeltypes.NewMsgChannelOpenAck(ctrlPort, ctrlChan, hostChan, hostCh.Version, proof, h, signer),
		channeltypes.NewMsgChannelOpenConfirm(icatypes.HostPortID, hostChan, proof, h, signer)))
	requireOK(t, fb.TxResults[0])

	addr, ok := e.app.ICAHostKeeper.GetInterchainAccountAddress(e.ctx(), conn, ctrlPort)
	require.True(t, ok)
	return ctrlPort, ctrlChan, addr
}

// icaSend has the controller send one ICA tx per msg list and returns the
// packets.
func (e *capEnv) icaSend(msgs ...[]proto.Message) []channeltypes.Packet {
	e.t.Helper()
	var out []sdk.Msg
	for _, m := range msgs {
		data, err := icatypes.SerializeCosmosTx(e.app.AppCodec(), m, icatypes.EncodingProtobuf)
		require.NoError(e.t, err)
		out = append(out, icacontrollertypes.NewMsgSendTx(e.bech(e.userAddr()), ibcexported.LocalhostConnectionID,
			uint64(time.Hour), icatypes.InterchainAccountPacketData{Type: icatypes.EXECUTE_TX, Data: data}))
	}
	fb := e.finalize(e.signedTx(5_000_000, e.fee(50_000), out...))
	requireOK(e.t, fb.TxResults[0])
	var packets []channeltypes.Packet
	for _, ev := range eventsOf(fb.TxResults[0].Events, channeltypes.EventTypeSendPacket) {
		data, err := hex.DecodeString(ev[channeltypes.AttributeKeyDataHex])
		require.NoError(e.t, err)
		seq, err := strconv.ParseUint(ev[channeltypes.AttributeKeySequence], 10, 64)
		require.NoError(e.t, err)
		th, err := clienttypes.ParseHeight(ev[channeltypes.AttributeKeyTimeoutHeight])
		require.NoError(e.t, err)
		ts, err := strconv.ParseUint(ev[channeltypes.AttributeKeyTimeoutTimestamp], 10, 64)
		require.NoError(e.t, err)
		packets = append(packets, channeltypes.NewPacket(data, seq, ev[channeltypes.AttributeKeySrcPort], ev[channeltypes.AttributeKeySrcChannel],
			ev[channeltypes.AttributeKeyDstPort], ev[channeltypes.AttributeKeyDstChannel], th, ts))
	}
	require.Len(e.t, packets, len(msgs))
	return packets
}

// TestResultCapICAHost: msgs a controller chain runs on earth's ICA host are
// bounded like a callback. A contract execute whose output passes
// MaxPacketAppResultBytes (600 KiB; or 1.5 MiB, which at 0a292ae made the
// MsgRecvPacket itself fail with code 21, undeliverable) is acknowledged with
// an error and its output dropped; a long error is cut to 1 KiB in the
// ics27_packet event; a quiet execute succeeds. All in one relay tx.
func TestResultCapICAHost(t *testing.T) {
	e := newCapEnv(t, "result-cap-ica")
	_, _, ica := e.openICAChannel()
	bigAttr := e.deploy("big-attr", outputContract(attrPrefix, 600<<10, attrSuffix))
	exec := func(contract string) []proto.Message {
		return []proto.Message{&wasmtypes.MsgExecuteContract{Sender: ica, Contract: contract, Msg: []byte("{}")}}
	}
	packets := e.icaSend(exec(e.contracts["small"]), exec(bigAttr), exec(e.contracts["huge-attr"]), exec(e.contracts["long-error"]))

	relayerKey := secp256k1.GenPrivKeyFromSecret([]byte("result-cap-ica/validator"))
	relayer := e.bech(sdk.AccAddress(relayerKey.PubKey().Address()))
	const gas = 80_000_000
	fb := e.finalize(e.signedTxAs(relayerKey, gas, e.gasFee(gas), e.recvMsgs(relayer, packets)...))
	r := fb.TxResults[0]
	requireOK(t, r)

	acks := eventsOf(r.Events, channeltypes.EventTypeWriteAck)
	require.Len(t, acks, 4)
	require.Contains(t, ackOf(t, acks[0]), `"result"`, "a quiet execute succeeds")
	tooLarge := fmt.Sprintf("ABCI code: %d", sdkerrors.ErrTxTooLarge.ABCICode())
	require.Contains(t, ackOf(t, acks[1]), tooLarge, "600 KiB of output: an error acknowledgement")
	require.Contains(t, ackOf(t, acks[2]), tooLarge, "1.5 MiB of output: an error acknowledgement")
	require.Contains(t, ackOf(t, acks[3]), fmt.Sprintf("ABCI code: %d", wasmtypes.ErrExecuteFailed.ABCICode()))

	capped := errAttr(r.Events, eventTypePacketResultCapped, attributeKeyResultBytes)
	require.Len(t, capped, 2)
	for _, n := range capped {
		v, err := strconv.Atoi(n)
		require.NoError(t, err)
		require.Greater(t, v, resultcap.MaxPacketAppResultBytes)
	}
	icaErr := errAttr(r.Events, icatypes.EventTypePacket, icatypes.AttributeKeyAckError)
	require.Len(t, icaErr, 1, "the long error's ics27_packet event (the capped ones were dropped whole)")
	require.Contains(t, icaErr[0], "error truncated: ")
	require.LessOrEqual(t, len(icaErr[0]), resultcap.MaxErrorBytes+64)

	size := resultSize(t, r)
	t.Logf("ICA relay tx: %d result bytes, %d gas", size, r.GasUsed)
	require.Less(t, size, 64<<10, "no output of the over-cap executes is stored")
}

// TestResultCapTxTotal is R8-D-1 on the chain side: an ordinary tx's msgs
// share a 1 MiB total again (at 0a292ae only each msg was capped, and four
// ~1 MB executes stored 4.1 MB). Three 300 KiB executes pass and are paid
// for; four fail with code 21 and store a few hundred bytes. A tx that mixes
// a relay msg with an ordinary one is ordinary.
func TestResultCapTxTotal(t *testing.T) {
	e := newCapEnv(t, "result-cap-total")
	mid := e.deploy("mid-attr", outputContract(attrPrefix, 300<<10, attrSuffix))
	user := e.bech(e.userAddr())
	execs := func(n int) []sdk.Msg {
		var out []sdk.Msg
		for i := 0; i < n; i++ {
			out = append(out, &wasmtypes.MsgExecuteContract{Sender: user, Contract: mid, Msg: []byte("{}")})
		}
		return out
	}
	const gas = 60_000_000
	fb := e.finalize(e.signedTx(gas, e.gasFee(gas), execs(3)...))
	requireOK(t, fb.TxResults[0])
	require.Greater(t, resultSize(t, fb.TxResults[0]), 900<<10)
	require.GreaterOrEqual(t, fb.TxResults[0].GasUsed, int64((900<<10)-resultFreeBytes)*resultGasPerByte)

	fb = e.finalize(e.signedTx(gas, e.gasFee(gas), execs(4)...))
	r := fb.TxResults[0]
	require.Equal(t, sdkerrors.ErrTxTooLarge.ABCICode(), r.Code, r.Log)
	require.Contains(t, r.Log, "tx msg results are")
	require.Less(t, resultSize(t, r), 4<<10)

	// A relay msg does not make a mixed tx a relay tx.
	mixed := append(execs(4), &banktypes.MsgSend{FromAddress: user, ToAddress: user, Amount: e.fee(1)})
	mixed = append([]sdk.Msg{channeltypes.NewMsgRecvPacket(channeltypes.Packet{}, nil, clienttypes.ZeroHeight(), user)}, mixed...)
	require.False(t, isRelayTx(mixed))
	require.True(t, isRelayTx(mixed[:1]))
	require.False(t, isRelayTx(nil))
}

// TestResultCapRelayMsgsRouted: every relay type URL resolves to a routed msg
// of the built app, so a renamed or missing msg cannot silently drop out of
// the relay set.
func TestResultCapRelayMsgsRouted(t *testing.T) {
	e := initShieldedEnv(t)
	for _, u := range resultcap.RelayMsgTypeURLs {
		require.NotNil(t, e.app.MsgServiceRouter().HandlerByTypeURL(u), u)
	}
	require.True(t, resultcap.IsRelayMsg(sdk.MsgTypeURL(&channeltypes.MsgRecvPacket{})))
	require.False(t, resultcap.IsRelayMsg(sdk.MsgTypeURL(&wasmtypes.MsgExecuteContract{})))
	require.False(t, resultcap.IsRelayMsg(sdk.MsgTypeURL(&channeltypes.MsgChannelOpenInit{})))
}

// --- worst cases --------------------------------------------------------------

// answerSizes is a stored tx result's size and the JSON a node builds for it:
// RPC tx (CometBFT's encoding, which writes '<' as the 6-byte <) and LCD
// txs/{hash} (the SDK's TxResponse, which carries the tx too).
func (e *capEnv) answerSizes(txBytes []byte, r *abci.ExecTxResult) (raw, rpc, lcd int) {
	e.t.Helper()
	raw = resultSize(e.t, r)
	rt := &coretypes2.ResultTx{Hash: cmtbytes.HexBytes(cmttypes.Tx(txBytes).Hash()), Height: e.height, TxResult: *r, Tx: txBytes}
	bz, err := cmtjson.Marshal(rt)
	require.NoError(e.t, err)
	rpc = len(bz)
	tx, err := e.app.TxConfig().TxDecoder()(txBytes)
	require.NoError(e.t, err)
	anyTx := tx.(interface{ AsAny() *codectypes.Any }).AsAny()
	bz, err = e.app.AppCodec().MarshalJSON(sdk.NewResponseResultTx(rt, anyTx, e.now.Format(time.RFC3339)))
	require.NoError(e.t, err)
	return raw, rpc, len(bz)
}

// Contract outputs for the worst cases. ltOutput fills one attribute with
// '<', whose JSON is six times its stored size, to about counted bytes as the
// caps count them. tinyOutput emits attributes {"key":"a","value":""}, the
// most JSON structure per counted byte (wasmd refuses an empty key, not an
// empty value): such an attribute counts 11 bytes and its JSON is 36,
// {"key":"a","value":"","index":true} and a comma.
func ltOutput(counted int, callback bool) []byte {
	return buildOutputContract(attrPrefix, counted/resultcap.JSONEscapeBytes, '<', attrSuffix, callback)
}

const (
	tinyAttr        = `{"key":"a","value":""}`
	tinyAttrCounted = 11
)

func tinyOutput(counted int, callback bool) []byte {
	n := counted / tinyAttrCounted
	return buildPatternContract(`{"ok":{"messages":[],"attributes":[`, (n-1)*(len(tinyAttr)+1), tinyAttr+",",
		tinyAttr+`],"events":[],"data":null}}`, callback)
}

// TestResultCapWorstCase measures the largest results the caps allow, for
// the fix report and the deploy repo's answer ceilings, with each of the two
// shapes whose JSON is largest for what they are counted: '<'-filled
// attributes (escaping, counted at its JSON size) and tiny attributes (JSON
// structure, which is not counted):
//   - an ordinary tx at the 1 MiB total;
//   - a free-tier tx, as many as a 100M-gas block holds (block_results);
//   - one ICS-20 MsgRecvPacket with a 32 KiB memo of '<';
//   - a relay tx at the 100M block gas limit, of ICS-20 packets with loud
//     destination callbacks over the localhost connection, which any account
//     can open.
func TestResultCapWorstCase(t *testing.T) {
	e := newCapEnv(t, "result-cap-worst")
	user := e.bech(e.userAddr())
	shapes := []struct {
		name  string
		build func(counted int, callback bool) []byte
	}{{"lt", ltOutput}, {"tiny", tinyOutput}}

	t.Run("ordinary tx", func(t *testing.T) {
		// One execute that takes the tx to just under the total: the wasm
		// event's other attribute, the message event and the counting
		// overheads are ~500 bytes.
		for _, sh := range shapes {
			c := e.deploy("worst-"+sh.name, sh.build(resultcap.MaxTxResultBytes-2048, false))
			const gas = 60_000_000
			tx := e.signedTx(gas, e.gasFee(gas), &wasmtypes.MsgExecuteContract{Sender: user, Contract: c, Msg: []byte("{}")})
			r := e.finalize(tx).TxResults[0]
			requireOK(t, r)
			raw, rpc, lcd := e.answerSizes(tx, r)
			t.Logf("ordinary tx (%s): %d B stored, RPC tx %d B, LCD txs/{hash} %d B, %d gas, tx %d B", sh.name, raw, rpc, lcd, r.GasUsed, len(tx))
			// The JSON is within the documented factor of what was counted.
			require.LessOrEqual(t, 10*rpc, resultcap.JSONPerCountedByteX10*(resultcap.MaxTxResultBytes+4<<10))
		}
	})

	t.Run("free-tier tx", func(t *testing.T) {
		// The most result per gas a block can hold: txs that stay inside
		// the free tier, as many as block gas allows (block_results' worst).
		for _, sh := range shapes {
			c := e.deploy("worst-free-"+sh.name, sh.build(resultcap.FreeBytes-1024, false))
			const gas = 1_000_000
			tx := e.signedTx(gas, e.gasFee(gas), &wasmtypes.MsgExecuteContract{Sender: user, Contract: c, Msg: []byte("{}")})
			r := e.finalize(tx).TxResults[0]
			requireOK(t, r)
			bz, err := cmtjson.Marshal(r)
			require.NoError(t, err)
			perBlock := 100_000_000 / r.GasUsed
			t.Logf("free-tier tx (%s): %d B stored, %d B in block_results JSON, %d gas: %d per 100M block = %d B stored, %d B JSON",
				sh.name, resultSize(t, r), len(bz), r.GasUsed, perBlock, perBlock*int64(resultSize(t, r)), perBlock*int64(len(bz)))
		}
	})

	e.openTransferChannel()
	receiver := e.bech(sdk.AccAddress([]byte("ibc-receiver-account")))
	relayerKey := secp256k1.GenPrivKeyFromSecret([]byte("result-cap-worst/validator"))
	relayer := e.bech(sdk.AccAddress(relayerKey.PubKey().Address()))
	relay := func(t *testing.T, gas uint64, packets []channeltypes.Packet) ([]byte, *abci.ExecTxResult) {
		tx := e.signedTxAs(relayerKey, gas, e.gasFee(gas), e.recvMsgs(relayer, packets)...)
		r := e.finalize(tx).TxResults[0]
		requireOK(t, r)
		require.Len(t, eventsOf(r.Events, channeltypes.EventTypeWriteAck), len(packets))
		return tx, r
	}
	callbacksOK := func(t *testing.T, r *abci.ExecTxResult, n int) {
		evs := eventsOf(r.Events, ibccallbackstypes.EventTypeDestinationCallback)
		require.Len(t, evs, n)
		for _, ev := range evs {
			require.Equal(t, ibccallbackstypes.AttributeValueCallbackSuccess, ev[ibccallbackstypes.AttributeKeyCallbackResult], ev[ibccallbackstypes.AttributeKeyCallbackError])
		}
		require.Empty(t, eventsOf(r.Events, "ibccallbackerror-"+eventTypePacketResultCapped))
	}

	t.Run("relay msg", func(t *testing.T) {
		// ICS-20 packet data is JSON that escapes '<' too, so a memo of
		// 32 KiB of '<' is ~197 KB of packet data, which core logs
		// hex-encoded twice; ICS-20 logs the memo itself once.
		packets := e.transfer(receiver, strings.Repeat("<", transfertypes.MaximumMemoLength))
		tx, r := relay(t, 40_000_000, packets)
		raw, rpc, lcd := e.answerSizes(tx, r)
		t.Logf("one MsgRecvPacket, 32 KiB memo of '<': %d B stored, RPC tx %d B, LCD txs/{hash} %d B, %d gas, packet data %d B",
			raw, rpc, lcd, r.GasUsed, len(packets[0].Data))

		// The same memo naming a destination callback that emits just
		// under MaxCallbackResultBytes: within MaxPacketAppResultBytes, so
		// the packet is received and the callback succeeds.
		loud := e.deploy("worst-cb-memo", ltOutput(resultcap.MaxCallbackResultBytes-4096, true))
		memo := callbackMemo(t, loud, 0)
		memo = strings.TrimSuffix(memo, "}") + `,"pad":"` + strings.Repeat("<", transfertypes.MaximumMemoLength-len(memo)-len(`,"pad":""}`)+1) + `"}`
		require.Len(t, memo, transfertypes.MaximumMemoLength)
		packets = e.transfer(receiver, memo)
		tx, r = relay(t, 60_000_000, packets)
		callbacksOK(t, r, 1)
		raw, rpc, lcd = e.answerSizes(tx, r)
		t.Logf("one MsgRecvPacket, 32 KiB memo of '<' and a loud '<' callback: %d B stored, RPC tx %d B, LCD txs/{hash} %d B, %d gas",
			raw, rpc, lcd, r.GasUsed)
	})

	t.Run("relay tx", func(t *testing.T) {
		// The most JSON per gas a relay tx can hold: packets whose
		// destination callback emits just under MaxCallbackResultBytes, as
		// many as the 100M block gas limit pays for.
		const gas = 100_000_000 // genesis block max_gas
		for _, sh := range shapes {
			loud := e.deploy("worst-cb-"+sh.name, sh.build(resultcap.MaxCallbackResultBytes-4096, true))
			const n = 16
			var packets []channeltypes.Packet
			for len(packets) < n {
				k := min(5, n-len(packets))
				packets = append(packets, e.transfer(receiver, repeat(callbackMemo(t, loud, 0), k)...)...)
			}
			tx, r := relay(t, gas, packets)
			callbacksOK(t, r, n)
			raw, rpc, lcd := e.answerSizes(tx, r)
			t.Logf("relay tx of %d loud-callback packets (%s): %d B stored, RPC tx %d B, LCD txs/{hash} %d B, %d gas, tx %d B",
				n, sh.name, raw, rpc, lcd, r.GasUsed, len(tx))
			require.LessOrEqual(t, uint64(raw), resultcap.MaxRelayTxResultBytes(gas)+4<<10)
			require.LessOrEqual(t, uint64(10*rpc), resultcap.JSONPerCountedByteX10*(resultcap.MaxRelayTxResultBytes(uint64(r.GasUsed))+16<<10))
		}
	})
}

// --- the IBC wrappers on their own ------------------------------------------

// fakeApp is an IBC application (v1 and v2) that writes a key, emits one
// event of size bytes (or an error attribute of that size, when asErr) and
// acknowledges with ack.
type fakeApp struct {
	porttypes.IBCModule
	key   *storetypes.KVStoreKey
	size  int
	asErr bool
	ack   []byte
}

func (f fakeApp) emit(ctx sdk.Context) {
	ctx.KVStore(f.key).Set([]byte("touched"), []byte{1})
	if f.asErr {
		ctx.EventManager().EmitEvent(sdk.NewEvent(icatypes.EventTypePacket,
			sdk.NewAttribute(icatypes.AttributeKeyAckError, strings.Repeat("e", f.size))))
		return
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("blob", sdk.NewAttribute("v", strings.Repeat("x", f.size))))
}

func (f fakeApp) OnRecvPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ sdk.AccAddress) ibcexported.Acknowledgement {
	f.emit(ctx)
	return channeltypes.NewResultAcknowledgement(f.ack)
}

func (f fakeApp) OnAcknowledgementPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ []byte, _ sdk.AccAddress) error {
	f.emit(ctx)
	return nil
}

func (f fakeApp) OnTimeoutPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ sdk.AccAddress) error {
	f.emit(ctx)
	return nil
}

type fakeAppV2 struct {
	fakeApp
	ibcapi.IBCModule
}

func (f fakeAppV2) OnRecvPacket(ctx sdk.Context, _, _ string, _ uint64, _ channeltypesv2.Payload, _ sdk.AccAddress) channeltypesv2.RecvPacketResult {
	f.emit(ctx)
	return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Success, Acknowledgement: f.ack}
}

func (f fakeAppV2) OnAcknowledgementPacket(ctx sdk.Context, _, _ string, _ uint64, _ []byte, _ channeltypesv2.Payload, _ sdk.AccAddress) error {
	f.emit(ctx)
	return nil
}

func (f fakeAppV2) OnTimeoutPacket(ctx sdk.Context, _, _ string, _ uint64, _ channeltypesv2.Payload, _ sdk.AccAddress) error {
	f.emit(ctx)
	return nil
}

// TestResultCapBoundedIBCModule: the route wrappers keep an application's
// state and events within the cap, cut its error attributes, and past the cap
// drop both: an error acknowledgement (v1) or a failed result (v2) on
// receive; on acknowledgement and timeout an error for a contract port and
// nothing for the others.
func TestResultCapBoundedIBCModule(t *testing.T) {
	e := initShieldedEnv(t)
	key := e.app.GetKey(banktypes.StoreKey)
	base, _ := e.ctx().CacheContext()
	fresh := func() sdk.Context {
		ctx, _ := base.CacheContext()
		return ctx.WithEventManager(sdk.NewEventManager())
	}
	touched := func(ctx sdk.Context) bool { return ctx.KVStore(key).Has([]byte("touched")) }
	packet := channeltypes.Packet{Sequence: 7, DestinationPort: "p", DestinationChannel: "channel-9"}
	under := maxPacketAppResultBytes - 4096
	over := maxPacketAppResultBytes + 1

	for _, v2 := range []bool{false, true} {
		recv := func(ctx sdk.Context, app fakeApp) bool {
			if v2 {
				res := boundedIBCModuleV2{IBCModule: fakeAppV2{fakeApp: app}}.OnRecvPacket(ctx, "a", "b", 7, channeltypesv2.Payload{DestinationPort: "p"}, nil)
				return res.Status == channeltypesv2.PacketStatus_Success
			}
			ack := boundedIBCModule{IBCModule: app}.OnRecvPacket(ctx, "", packet, nil)
			if !ack.Success() {
				require.Contains(t, string(ack.Acknowledgement()), fmt.Sprintf("ABCI code: %d", sdkerrors.ErrTxTooLarge.ABCICode()))
			}
			return ack.Success()
		}
		ackT := func(ctx sdk.Context, app fakeApp, contractPort bool) error {
			if v2 {
				return boundedIBCModuleV2{IBCModule: fakeAppV2{fakeApp: app}, contractPort: contractPort}.OnAcknowledgementPacket(ctx, "a", "b", 7, nil, channeltypesv2.Payload{}, nil)
			}
			return boundedIBCModule{IBCModule: app, contractPort: contractPort}.OnAcknowledgementPacket(ctx, "", packet, nil, nil)
		}
		timeoutT := func(ctx sdk.Context, app fakeApp, contractPort bool) error {
			if v2 {
				return boundedIBCModuleV2{IBCModule: fakeAppV2{fakeApp: app}, contractPort: contractPort}.OnTimeoutPacket(ctx, "a", "b", 7, channeltypesv2.Payload{}, nil)
			}
			return boundedIBCModule{IBCModule: app, contractPort: contractPort}.OnTimeoutPacket(ctx, "", packet, nil)
		}
		t.Run(fmt.Sprintf("v2=%t", v2), func(t *testing.T) {
			ctx := fresh()
			require.True(t, recv(ctx, fakeApp{key: key, size: under}))
			require.True(t, touched(ctx))
			require.Len(t, eventsOf(ctx.EventManager().ABCIEvents(), "blob"), 1)

			// The acknowledgement counts twice: under the cap in events,
			// over it with the ack.
			ctx = fresh()
			require.False(t, recv(ctx, fakeApp{key: key, size: under, ack: make([]byte, 4096)}))
			require.False(t, touched(ctx))
			require.Empty(t, eventsOf(ctx.EventManager().ABCIEvents(), "blob"))
			require.Len(t, eventsOf(ctx.EventManager().ABCIEvents(), eventTypePacketResultCapped), 1)

			ctx = fresh()
			require.False(t, recv(ctx, fakeApp{key: key, size: over}))
			require.False(t, touched(ctx))

			// A long error attribute is cut, not capped.
			ctx = fresh()
			require.True(t, recv(ctx, fakeApp{key: key, size: 200 << 10, asErr: true}))
			errs := eventsOf(ctx.EventManager().ABCIEvents(), icatypes.EventTypePacket)
			require.Len(t, errs, 1)
			require.Contains(t, errs[0][icatypes.AttributeKeyAckError], "error truncated: 204800 bytes")

			for _, cb := range []func(sdk.Context, fakeApp, bool) error{ackT, timeoutT} {
				ctx = fresh()
				require.NoError(t, cb(ctx, fakeApp{key: key, size: over}, false), "ICS-20, ICA: never failed for size")
				require.True(t, touched(ctx))
				ctx = fresh()
				require.ErrorIs(t, cb(ctx, fakeApp{key: key, size: over}, true), sdkerrors.ErrTxTooLarge, "a contract port")
				require.False(t, touched(ctx))
				ctx = fresh()
				require.NoError(t, cb(ctx, fakeApp{key: key, size: under}, true))
				require.True(t, touched(ctx))
			}
		})
	}
}

// failingCallbacks is a callback contract keeper whose every callback fails
// with a ~0.9 MB error.
type failingCallbacks struct {
	ibccallbackstypes.ContractKeeper
}

var errHuge = wasmtypes.ErrExecuteFailed.Wrap(strings.Repeat("x", 900_000))

func (failingCallbacks) IBCSendPacketCallback(sdk.Context, string, string, clienttypes.Height, uint64, []byte, string, string, string) error {
	return errHuge
}

func (failingCallbacks) IBCOnAcknowledgementPacketCallback(sdk.Context, channeltypes.Packet, []byte, sdk.AccAddress, string, string, string) error {
	return errHuge
}

func (failingCallbacks) IBCOnTimeoutPacketCallback(sdk.Context, channeltypes.Packet, sdk.AccAddress, string, string, string) error {
	return errHuge
}

func (failingCallbacks) IBCReceivePacketCallback(sdk.Context, ibcexported.PacketI, ibcexported.Acknowledgement, string, string) error {
	return errHuge
}

// TestResultCapCallbackErrors: every callback's error is cut to 1 KiB with
// its code kept, the source-side ones too (R8-C-1: ibc-go ignores an
// ack/timeout callback's error but still writes it into callback_error of
// the relayer's MsgAcknowledgement / MsgTimeout).
func TestResultCapCallbackErrors(t *testing.T) {
	c := cappedCallbacks{failingCallbacks{}}
	ctx := capCtx(nil, storetypes.NewInfiniteGasMeter())
	for name, err := range map[string]error{
		"send":    c.IBCSendPacketCallback(ctx, "", "", clienttypes.ZeroHeight(), 0, nil, "", "", ""),
		"ack":     c.IBCOnAcknowledgementPacketCallback(ctx, channeltypes.Packet{}, nil, nil, "", "", ""),
		"timeout": c.IBCOnTimeoutPacketCallback(ctx, channeltypes.Packet{}, nil, "", "", ""),
		"receive": c.IBCReceivePacketCallback(ctx, channeltypes.Packet{}, nil, "", ""),
	} {
		require.ErrorIs(t, err, wasmtypes.ErrExecuteFailed, name)
		require.LessOrEqual(t, len(err.Error()), resultcap.MaxErrorBytes+64, name)
	}
}
