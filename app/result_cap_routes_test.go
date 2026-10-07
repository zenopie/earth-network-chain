package app

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	localhost "github.com/cosmos/ibc-go/v10/modules/light-clients/09-localhost"
	"github.com/stretchr/testify/require"

	assemblytypes "github.com/earth-network/earth/x/assembly/types"
)

// End-to-end coverage of the result cap on the routes a msg reaches other
// than as a plain top-level msg (round 7): an ICS-20 relay batch over a
// localhost channel, an IBC destination callback, authz MsgExec, a contract's
// dispatched submessage, and gov proposal execution in EndBlock.

// deploy stores and instantiates one contract and returns its address.
func (e *capEnv) deploy(name string, code []byte) string {
	e.t.Helper()
	sender := e.bech(e.userAddr())
	fb := e.finalize(e.signedTx(20_000_000, e.fee(100_000), &wasmtypes.MsgStoreCode{Sender: sender, WASMByteCode: code}))
	requireOK(e.t, fb.TxResults[0])
	id, err := strconv.ParseUint(eventsOf(fb.TxResults[0].Events, wasmtypes.EventTypeStoreCode)[0][wasmtypes.AttributeKeyCodeID], 10, 64)
	require.NoError(e.t, err)
	fb = e.finalize(e.signedTx(5_000_000, e.fee(25_000), &wasmtypes.MsgInstantiateContract{
		Sender: sender, CodeID: id, Label: name, Msg: []byte("{}"),
	}))
	requireOK(e.t, fb.TxResults[0])
	addr := eventsOf(fb.TxResults[0].Events, wasmtypes.EventTypeInstantiate)[0][wasmtypes.AttributeKeyContractAddr]
	e.contracts[name] = addr
	return addr
}

// gasFee pays for gas at 0.01uerth, twice the env's minimum price.
func (e *capEnv) gasFee(gas uint64) sdk.Coins { return e.fee(int64(gas / 100)) }

// msgBytes sums the stored size of each msg's events by msg_index.
func msgBytes(evs []abci.Event) map[string]int {
	out := map[string]int{}
	for _, ev := range evs {
		for _, a := range ev.Attributes {
			if a.Key == "msg_index" {
				out[a.Value] += ev.Size()
			}
		}
	}
	return out
}

// --- IBC over the localhost client ------------------------------------------

// openTransferChannel opens transfer/channel-0 <-> transfer/channel-1 over
// ibc-go's 09-localhost connection: the chain is its own counterparty, and
// every proof is the localhost sentinel (the client reads the chain's own
// store). Real handshake, real packet commitments, real MsgRecvPacket.
func (e *capEnv) openTransferChannel() {
	e.t.Helper()
	signer := e.bech(e.userAddr())
	h := clienttypes.GetSelfHeight(e.ctx())
	hops := []string{ibcexported.LocalhostConnectionID}
	proof := localhost.SentinelProof
	msgs := []sdk.Msg{
		channeltypes.NewMsgChannelOpenInit(transfertypes.PortID, transfertypes.V1, channeltypes.UNORDERED, hops, transfertypes.PortID, signer),
		channeltypes.NewMsgChannelOpenTry(transfertypes.PortID, transfertypes.V1, channeltypes.UNORDERED, hops, transfertypes.PortID, "channel-0", transfertypes.V1, proof, h, signer),
		channeltypes.NewMsgChannelOpenAck(transfertypes.PortID, "channel-0", "channel-1", transfertypes.V1, proof, h, signer),
		channeltypes.NewMsgChannelOpenConfirm(transfertypes.PortID, "channel-1", proof, h, signer),
	}
	fb := e.finalize(e.signedTx(2_000_000, e.fee(20_000), msgs...))
	requireOK(e.t, fb.TxResults[0])
	for _, id := range []string{"channel-0", "channel-1"} {
		ch, ok := e.app.IBCKeeper.ChannelKeeper.GetChannel(e.ctx(), transfertypes.PortID, id)
		require.True(e.t, ok)
		require.Equal(e.t, channeltypes.OPEN, ch.State, id)
	}
}

// transfer sends one ICS-20 packet per memo from channel-0 and returns the
// packets as committed (read back from send_packet).
func (e *capEnv) transfer(receiver string, memos ...string) []channeltypes.Packet {
	e.t.Helper()
	sender := e.bech(e.userAddr())
	timeout := uint64(e.now.Add(24 * time.Hour).UnixNano())
	var msgs []sdk.Msg
	for _, memo := range memos {
		msgs = append(msgs, transfertypes.NewMsgTransfer(transfertypes.PortID, "channel-0", sdk.NewInt64Coin("uerth", 1),
			sender, receiver, clienttypes.ZeroHeight(), timeout, memo))
	}
	const gas = 40_000_000
	fb := e.finalize(e.signedTx(gas, e.gasFee(gas), msgs...))
	requireOK(e.t, fb.TxResults[0])
	var out []channeltypes.Packet
	for _, ev := range eventsOf(fb.TxResults[0].Events, channeltypes.EventTypeSendPacket) {
		data, err := hex.DecodeString(ev[channeltypes.AttributeKeyDataHex])
		require.NoError(e.t, err)
		seq, err := strconv.ParseUint(ev[channeltypes.AttributeKeySequence], 10, 64)
		require.NoError(e.t, err)
		th, err := clienttypes.ParseHeight(ev[channeltypes.AttributeKeyTimeoutHeight])
		require.NoError(e.t, err)
		ts, err := strconv.ParseUint(ev[channeltypes.AttributeKeyTimeoutTimestamp], 10, 64)
		require.NoError(e.t, err)
		out = append(out, channeltypes.NewPacket(data, seq, ev[channeltypes.AttributeKeySrcPort], ev[channeltypes.AttributeKeySrcChannel],
			ev[channeltypes.AttributeKeyDstPort], ev[channeltypes.AttributeKeyDstChannel], th, ts))
	}
	require.Len(e.t, out, len(memos))
	return out
}

func (e *capEnv) recvMsgs(relayer string, packets []channeltypes.Packet) []sdk.Msg {
	h := clienttypes.GetSelfHeight(e.ctx())
	var msgs []sdk.Msg
	for _, p := range packets {
		msgs = append(msgs, channeltypes.NewMsgRecvPacket(p, localhost.SentinelProof, h, relayer))
	}
	return msgs
}

// TestResultCapRelayBatch is R7-C-1 end to end: a relayer's batch of 38
// MsgRecvPacket, eight of them carrying ibc-go's 32 KiB maximum memo, stores
// well over 1 MiB of results and is delivered whole, paying gas for the
// bytes. Under the round-6 per-tx cap the same batch failed. A tx with one
// over-cap msg in the same block fails alone.
func TestResultCapRelayBatch(t *testing.T) {
	e := newCapEnv(t, "result-cap-relay")
	e.openTransferChannel()

	// The relayer is a second account (the validator's), so its tx and the
	// user's share a block.
	relayerKey := secp256k1.GenPrivKeyFromSecret([]byte("result-cap-relay/validator"))
	relayer := e.bech(sdk.AccAddress(relayerKey.PubKey().Address()))
	receiver := e.bech(sdk.AccAddress([]byte("ibc-receiver-account")))

	const big, honest = 8, 30
	bigMemo := strings.Repeat("m", transfertypes.MaximumMemoLength)
	var packets []channeltypes.Packet
	packets = append(packets, e.transfer(receiver, repeat(bigMemo, big)...)...)
	packets = append(packets, e.transfer(receiver, repeat("", honest)...)...)

	// One honest packet and one max-memo packet alone, for their sizes.
	probe := func(p channeltypes.Packet) int {
		_, res, err := e.app.Simulate(e.signedTxAs(relayerKey, 10_000_000, e.gasFee(10_000_000), e.recvMsgs(relayer, []channeltypes.Packet{p})...))
		require.NoError(t, err)
		return eventBytesTest(res.Events)
	}
	honestSize, bigSize := probe(packets[big]), probe(packets[0])
	t.Logf("MsgRecvPacket result: %d bytes honest, %d bytes with a %d-byte memo", honestSize, bigSize, transfertypes.MaximumMemoLength)
	require.Greater(t, bigSize, 128<<10)
	require.Less(t, bigSize, 256<<10)

	const gas = 60_000_000
	relay := e.signedTxAs(relayerKey, gas, e.gasFee(gas), e.recvMsgs(relayer, packets)...)
	over := e.signedTx(10_000_000, e.fee(50_000), &wasmtypes.MsgExecuteContract{
		Sender: e.bech(e.userAddr()), Contract: e.contracts["huge-attr"], Msg: []byte("{}"),
	})
	fb := e.finalize(relay, over)
	r := fb.TxResults[0]
	requireOK(t, r)
	acks := eventsOf(r.Events, channeltypes.EventTypeWriteAck)
	require.Len(t, acks, big+honest, "every packet received in the one tx")
	for _, a := range acks {
		ack, err := hex.DecodeString(a[channeltypes.AttributeKeyAckHex])
		require.NoError(t, err)
		require.Contains(t, string(ack), `"result"`, "a success acknowledgement")
	}
	size := resultSize(t, r)
	t.Logf("relay batch of %d packets: %d result bytes, %d gas", big+honest, size, r.GasUsed)
	require.Greater(t, size, maxMsgResultBytes, "the batch's results pass what the round-6 per-tx cap allowed")
	require.Less(t, r.GasUsed, int64(gas))
	require.GreaterOrEqual(t, r.GasUsed, int64(size-resultFreeBytes)*resultGasPerByte*9/10, "the bytes are paid for")
	for idx, n := range msgBytes(r.Events) {
		require.Less(t, n, maxMsgResultBytes, "msg %s", idx)
	}
	bal := e.app.BankKeeper.GetAllBalances(e.ctx(), sdk.MustAccAddressFromBech32(receiver))
	require.Len(t, bal, 1)
	require.Equal(t, int64(big+honest), bal[0].Amount.Int64(), "every voucher minted")

	// The over-cap tx of the same block failed alone.
	require.Equal(t, sdkerrors.ErrTxTooLarge.ABCICode(), fb.TxResults[1].Code, fb.TxResults[1].Log)
	require.Equal(t, sdkerrors.ErrTxTooLarge.Codespace(), fb.TxResults[1].Codespace)
}

// TestResultCapIBCCallback: a destination callback that emits more than
// maxCallbackResultBytes fails, so the packet is received with an error
// acknowledgement (the sender is refunded) instead of making a MsgRecvPacket
// no relayer can ever deliver. The relay tx itself succeeds and stays small.
func TestResultCapIBCCallback(t *testing.T) {
	e := newCapEnv(t, "result-cap-callback")
	e.openTransferChannel()
	small := e.deploy("cb-small", callbackContract(attrPrefix, 1<<10, attrSuffix))
	loud := e.deploy("cb-loud", callbackContract(attrPrefix, 320<<10, attrSuffix))
	receiver := e.bech(sdk.AccAddress([]byte("ibc-receiver-account")))

	memo := func(addr string) string {
		bz, err := json.Marshal(map[string]any{"dest_callback": map[string]any{"address": addr}})
		require.NoError(t, err)
		return string(bz)
	}
	packets := e.transfer(receiver, memo(small), memo(loud))
	const gas = 20_000_000
	fb := e.finalize(e.signedTx(gas, e.gasFee(gas), e.recvMsgs(e.bech(e.userAddr()), packets)...))
	r := fb.TxResults[0]
	requireOK(t, r)
	require.Less(t, resultSize(t, r), 64<<10, "the loud callback's events are dropped")

	acks := eventsOf(r.Events, channeltypes.EventTypeWriteAck)
	require.Len(t, acks, 2)
	ackOf := func(i int) string {
		bz, err := hex.DecodeString(acks[i][channeltypes.AttributeKeyAckHex])
		require.NoError(t, err)
		return string(bz)
	}
	require.Contains(t, ackOf(0), `"result"`, "a quiet callback: success")
	require.Contains(t, ackOf(1), `"error"`, "an over-cap callback: error acknowledgement")
	require.Contains(t, ackOf(1), fmt.Sprintf("ABCI code: %d", sdkerrors.ErrTxTooLarge.ABCICode()))
	// The error ack reverts the receive and drops its events, the
	// callback's with them.
	cbs := eventsOf(r.Events, "ibc_dest_callback")
	require.Len(t, cbs, 1)
	require.Equal(t, small, cbs[0]["callback_address"])
	require.Equal(t, "success", cbs[0]["callback_result"])
	// Only the first packet's voucher exists.
	bal := e.app.BankKeeper.GetAllBalances(e.ctx(), sdk.MustAccAddressFromBech32(receiver))
	require.Len(t, bal, 1)
	require.Equal(t, int64(1), bal[0].Amount.Int64())
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func eventBytesTest(evs []abci.Event) int {
	n := 0
	for _, ev := range evs {
		n += ev.Size()
	}
	return n
}

// --- authz and a contract's submessage ---------------------------------------

// TestResultCapNestedRoutes: output reached through authz MsgExec or through
// a contract's dispatched submessage is counted in the top-level msg: over
// the cap it fails the tx, under it it is paid for.
func TestResultCapNestedRoutes(t *testing.T) {
	e := newCapEnv(t, "result-cap-nested")
	user := e.bech(e.userAddr())
	bigAttr := e.deploy("big-attr", outputContract(attrPrefix, 600<<10, attrSuffix))
	dispatch := func(target string) string {
		prefix := fmt.Sprintf(`{"ok":{"messages":[{"id":0,"msg":{"wasm":{"execute":{"contract_addr":%q,"msg":"e30=","funds":[]}}},"gas_limit":null,"reply_on":"never"}],"attributes":[],"events":[],"data":null}}`, target)
		return e.deploy("dispatch-"+target, outputContract(prefix, 0, ""))
	}
	exec := func(contract string) *wasmtypes.MsgExecuteContract {
		return &wasmtypes.MsgExecuteContract{Sender: user, Contract: contract, Msg: []byte("{}")}
	}
	run := func(msg sdk.Msg) *abci.ExecTxResult {
		const gas = 30_000_000
		fb := e.finalize(e.signedTx(gas, e.gasFee(gas), msg))
		return fb.TxResults[0]
	}
	paid := func(t *testing.T, r *abci.ExecTxResult) {
		requireOK(t, r)
		require.Greater(t, resultSize(t, r), 600<<10)
		require.GreaterOrEqual(t, r.GasUsed, int64((600<<10)-resultFreeBytes)*resultGasPerByte)
	}
	tooLarge := func(t *testing.T, r *abci.ExecTxResult) {
		require.Equal(t, sdkerrors.ErrTxTooLarge.ABCICode(), r.Code, r.Log)
		require.Less(t, resultSize(t, r), 4<<10)
	}

	t.Run("authz", func(t *testing.T) {
		// The grantee is the msgs' signer: authz needs no grant for that.
		ok := authz.NewMsgExec(e.userAddr(), []sdk.Msg{exec(bigAttr)})
		paid(t, run(&ok))
		over := authz.NewMsgExec(e.userAddr(), []sdk.Msg{exec(e.contracts["huge-attr"])})
		tooLarge(t, run(&over))
	})
	t.Run("submessage", func(t *testing.T) {
		paid(t, run(exec(dispatch(bigAttr))))
		tooLarge(t, run(exec(dispatch(e.contracts["huge-attr"]))))
	})
}

// --- gov ---------------------------------------------------------------------

// TestResultCapGovEndBlock is R7-C-2 end to end: gov runs a passed
// proposal's msgs in EndBlock, without gas; one meter per EndBlock caps them
// together, so a proposal whose msgs pass 1 MiB in total fails (FAILED, its
// state reverted) and one with a single 600 KiB msg passes.
func TestResultCapGovEndBlock(t *testing.T) {
	e := newCapEnv(t, "result-cap-gov")
	_, ok := e.app.ModuleManager.Modules[govtypes.ModuleName].(meteredGovModule)
	require.True(t, ok, "gov's EndBlock is metered")
	bigAttr := e.deploy("big-attr", outputContract(attrPrefix, 600<<10, attrSuffix))
	govAddr := e.bech(authtypes.NewModuleAddress(govtypes.ModuleName))
	valKey := secp256k1.GenPrivKeyFromSecret([]byte("result-cap-gov/validator"))

	pass := func(n int) govv1.ProposalStatus {
		var msgs []sdk.Msg
		for i := 0; i < n; i++ {
			msgs = append(msgs, &wasmtypes.MsgExecuteContract{Sender: govAddr, Contract: bigAttr, Msg: []byte("{}")})
		}
		sub, err := govv1.NewMsgSubmitProposal(msgs, e.fee(1_000_000), e.bech(e.userAddr()), "", "loud", "loud", false)
		require.NoError(t, err)
		fb := e.finalize(e.signedTx(2_000_000, e.fee(20_000), sub))
		requireOK(t, fb.TxResults[0])
		id, err := strconv.ParseUint(eventsOf(fb.TxResults[0].Events, "submit_proposal")[0]["proposal_id"], 10, 64)
		require.NoError(t, err)
		// Stake: the only validator votes yes.
		fb = e.finalize(e.signedTxAs(valKey, 300_000, e.fee(5_000), govv1.NewMsgVote(sdk.AccAddress(valKey.PubKey().Address()), id, govv1.OptionYes, "")))
		requireOK(t, fb.TxResults[0])
		// Humans: one yes on the proposal's ballot (the vote's proof is
		// covered by the personhood tests; this test is about execution).
		ctx := e.ctx()
		// The ballot opens with the first vote, as MsgVoteProposal does.
		ballot := 1000 + id
		require.NoError(t, e.app.AssemblyKeeper.ProposalBallot.Set(ctx, id, ballot))
		require.NoError(t, e.app.AssemblyKeeper.BallotTally.Set(ctx, ballot, assemblytypes.Tally{Yes: 1}))
		e.finalizeAfter(7*24*time.Hour + time.Minute)
		p, err := e.app.GovKeeper.Proposals.Get(e.ctx(), id)
		require.NoError(t, err)
		if p.Status == govv1.StatusFailed {
			require.Contains(t, p.FailedReason, "proposal msgs of this block")
		}
		return p.Status
	}
	require.Equal(t, govv1.StatusPassed, pass(1), "one 600 KiB msg")
	require.Equal(t, govv1.StatusFailed, pass(2), "two: 1.2 MiB in one EndBlock")
}
