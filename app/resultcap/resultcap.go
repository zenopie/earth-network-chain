// Package resultcap holds the consensus limits on what a transaction, an
// EndBlock or an IBC packet handler may leave in the chain's ABCI results
// (round-6 R6-E-1, round-7 R7-C-1/R7-C-2, round-8 R8-C-1/R8-D-1). The
// enforcement is in package app (result_cap.go); the numbers are here, with no
// imports, so that tools outside the chain (the deploy repo's edge
// conformance test, which sizes its answer ceilings from them) can read them
// from the pinned chain source.
//
// Every value is consensus: changing one is a state-machine change.
//
// # Why
//
// A tx's ExecTxResult (events with their attributes, the msg responses, the
// log) is stored twice by every node (the ABCI responses and the tx index)
// and kept for ever, and every block_results / tx / tx-by-hash answer
// rebuilds it in memory at several times its stored size. Nothing in the SDK
// bounds it: block max_bytes bounds tx bytes, not results, and wasmd charges
// about one gas per byte of contract events and nothing per byte of response
// data.
//
// # The rules
//
// Bytes are counted as described under "How bytes are counted" below: in
// short, the proto size of a msg's events and responses, every string byte
// at the size a node's JSON answer gives it, plus EventOverhead,
// AttributeOverhead and MsgOverhead. "Bytes" in the rules and limits below
// are counted bytes. The ante phase is not metered (a signed tx's ante events
// are a fixed handful, under 1 KB; a private tx's are a function of its
// shape, 6.5 KB at most, and capped per block by
// max_private_actions_per_block).
//
//   - Gas. The first FreeBytes of a tx's msg-phase result are free (per tx,
//     not per msg); every byte past that costs GasPerByte gas on the tx's gas
//     meter. A tx's result is therefore at most FreeBytes + gas_limit /
//     GasPerByte, and a block's paid bytes at most max_gas / GasPerByte.
//
//   - Ordinary txs. A tx that has any msg other than a relay msg (see
//     RelayMsgTypeURLs) fails with ErrTxTooLarge (code 21) when its msgs'
//     results pass MaxTxResultBytes in total, or one msg's passes
//     MaxMsgResultBytes. Its msgs' state is reverted, its fee charged, and
//     what is stored is its ante events and a one-line log.
//
//   - Relay txs. A tx whose msgs are all relay msgs has no byte cap, only the
//     gas above. A relayer's batch is then never failed by its size, and no
//     packet can be made one that no relayer can deliver by what it makes
//     earth emit (R8-C-1): a large one only costs more gas. What a packet's
//     contents can put into a relay msg's result is bounded instead at its
//     source, by the next three rules.
//
//   - IBC applications. Whatever an IBC application emits while handling one
//     received packet (its events, nested msgs' included, plus its
//     acknowledgement, counted twice since core stores it hex-encoded) is at
//     most MaxPacketAppResultBytes. Past that the packet is acknowledged
//     with an error (the sender is refunded) and the application's events
//     and state are dropped. This covers ICS-20 (with its callbacks), the
//     ICA host (msgs a controller chain runs on earth) and contract-owned
//     ports, over IBC v1 and v2. A contract-owned port's acknowledgement or
//     timeout handler past the cap fails the msg, as the contract returning
//     an error would.
//
//   - IBC callbacks. A callback contract named in a packet's memo may emit at
//     most MaxCallbackResultBytes of events; past that the callback fails (on
//     receive: an error acknowledgement; on acknowledgement or timeout: only
//     the callback is dropped).
//
//   - Errors. An error text that reaches a result (a failed tx's log, a
//     nested msg's error, an IBC callback's callback_error, an ICA host or
//     contract port's error attribute) is cut to MaxErrorBytes; its ABCI
//     codespace and code are kept. A contract chooses its own error string,
//     up to its memory, so without this a failing callback could put ~0.9 MB
//     into a MsgRecvPacket (R8-C-1).
//
//   - Gov. Proposal msgs run in EndBlock, without gas. All the msgs of all
//     the proposals executed in one EndBlock share MaxEndBlockResultBytes;
//     past that the msg fails and its proposal is marked FAILED. Each msg is
//     also capped at MaxMsgResultBytes.
//
// # How bytes are counted
//
// A result is stored as proto, but it is served as JSON (CometBFT RPC
// block_results, tx and tx_search; the SDK's LCD txs/{hash} and txs), and the
// JSON is what a node and a client build in memory. The bytes are therefore
// counted at their worst-case JSON size, so that gas and the caps bound the
// answers' content, not only the store:
//
//   - Event strings (types, keys, values), byte by byte, at the largest size
//     any of these answers' encoders gives them. All of them render strings
//     with Go's encoding/json: CometBFT's RPC (gogoproto jsonpb inside
//     cmtjson, then the stdlib encoder's HTML-safe compaction) and the LCD
//     (the gRPC gateway's gogoproto jsonpb); protojson, where used, escapes a
//     subset. So '<', '>', '&', a control character other than \b \f \n \r
//     \t, and each byte of invalid UTF-8 (written as \ufffd) count
//     JSONEscapeBytes (6); '"', '\' and those five count 2; U+2028 and U+2029
//     (3 bytes) count 6; every other byte counts 1. An ASCII string free of
//     these, which is every string of the chain's own flows, counts its
//     length: those flows cost what they did.
//   - Msg responses, stored as bytes in ExecTxResult.Data, count
//     ResponseByteWeight (2) per proto byte: the LCD writes them as hex
//     (tx_response.data), the RPC as base64.
//   - The proto framing (tags, lengths, index flags) and the overheads count
//     once, as before.
//
// The count is one pass over each string, with no allocation, and a pure
// function of the result: deterministic. Error texts are cut by the same
// count (MaxErrorBytes of JSON).
//
// What is not counted is the JSON structure around the strings (field names,
// quotes, braces). It makes an answer at most JSONPerCountedByteX10/10 (3.3)
// times its counted bytes, for a contract that emits only attributes with a
// one-byte key and an empty value (11 counted bytes, 36 of JSON each); an
// ordinary attribute's structure is a small fraction of it. Counting it
// would raise every ordinary result's cost.
//
// # Worst cases
//
// At genesis block max_gas 100M, as measured by app's TestResultCapWorstCase
// with the two shapes whose JSON is largest for their count: attributes
// filled with '<' (escaping, now counted: JSON ~= counted) and attributes
// {"key":"a","value":""} (structure, ~3.3x). Stored is the proto
// ExecTxResult; RPC is the tx answer, block_results is about the same per
// tx; LCD's txs/{hash} adds the tx's JSON.
//
//   - Any tx with a non-relay msg: MaxTxResultBytes counted, plus its ante
//     events. '<': 176 KB stored, 1.05 MB RPC. Tiny attributes: 667 KB
//     stored, 3.43 MB RPC.
//   - One relay msg: core logs a packet's data hex-encoded twice (~4x its
//     size), plus at most MaxPacketAppResultBytes from the application. An
//     ICS-20 packet with 32 KiB of '<' as its memo has ~197 KB of data (its
//     JSON escapes '<' too): 823 KB stored and 1.25 MB RPC without a
//     callback, 865 KB and 1.51 MB with a loud '<' one. Any relay msg is
//     also within the relay tx bound.
//   - A relay tx: MaxRelayTxResultBytes(gas_limit) counted, 5,008,192 at
//     100M gas, plus its ante events, so at most ~16.4 MB of RPC JSON (tiny
//     attributes). Measured, 16 packets whose callbacks emit just under
//     MaxCallbackResultBytes: '<' 760 KB stored, 4.25 MB RPC at 86.6M gas;
//     tiny attributes 2.70 MB stored, 13.6 MB RPC at 83.3M gas.
//   - A block: paid bytes and free tiers share block gas. Free-tier txs fill
//     a block with the most: '<' ~827 txs, 1.9 MB stored, 7.6 MB of
//     block_results JSON; tiny attributes ~708 txs, 4.0 MB stored, 18.0 MB
//     JSON. All-paid is at most 5 MB counted. Gov's EndBlock adds at most
//     MaxEndBlockResultBytes counted (~3.4 MB of JSON).
package resultcap

const (
	// MaxTxResultBytes caps the msg results of one tx that has any non-relay
	// msg.
	MaxTxResultBytes = 1 << 20 // 1 MiB

	// MaxMsgResultBytes caps one non-relay top-level msg's result (its
	// events, nested msgs' included, and its response), in a tx or in gov's
	// EndBlock.
	MaxMsgResultBytes = 1 << 20 // 1 MiB

	// MaxEndBlockResultBytes caps the results of every gov-proposal msg run
	// in one EndBlock together.
	MaxEndBlockResultBytes = 1 << 20 // 1 MiB

	// MaxPacketAppResultBytes caps what an IBC application emits for one
	// received packet: events plus twice its acknowledgement. Above
	// MaxCallbackResultBytes plus an ICS-20 packet's own events (~210 KB
	// counted with the largest memo, all '<'), so an ICS-20 receive within
	// the callback cap never trips it.
	MaxPacketAppResultBytes = 512 << 10 // 512 KiB

	// MaxCallbackResultBytes caps the events of one IBC callback.
	MaxCallbackResultBytes = 256 << 10 // 256 KiB

	// MaxErrorBytes caps an error text that reaches a result. Errors are a
	// line or two; a cheap failing contract call should not buy kilobytes.
	MaxErrorBytes = 1 << 10 // 1 KiB

	// FreeBytes is the per-tx allowance that costs no extra gas: 1.5x the
	// largest single msg of the chain's own flows (MsgRegister, 5.4 KB), so
	// no wallet's or gas-check's gas changes.
	FreeBytes = 8 << 10 // 8 KiB

	// GasPerByte prices the bytes past FreeBytes: twice the tx-bytes price
	// (auth TxSizeCostPerByte 10), as a result is stored twice.
	GasPerByte = 20

	// Overheads counted on top of the proto size of what a msg returns:
	// baseapp appends a msg_index attribute to every event, MarkEventsToIndex
	// sets every attribute's index flag, and each msg gets a "message" event
	// (action, sender, module). Generous, so that many tiny events are not
	// cheaper than a few large ones.
	EventOverhead     = 24
	AttributeOverhead = 4
	MsgOverhead       = 256

	// JSONEscapeBytes is the most bytes one stored byte of an event string
	// becomes in a node's JSON answers (see "How bytes are counted"): the
	// 6-byte \u003c for '<', and likewise for '>', '&', control characters
	// and each byte of invalid UTF-8 (\ufffd). Every limit above counts such
	// a byte as this many.
	JSONEscapeBytes = 6

	// ResponseByteWeight is what one byte of a msg response counts as: the
	// responses are stored as bytes (ExecTxResult.Data), which the LCD
	// writes as hex (tx_response.data) and the RPC as base64.
	ResponseByteWeight = 2

	// JSONPerCountedByteX10 is ten times the most JSON one counted byte of a
	// result becomes in a node's answers (the uncounted JSON structure; see
	// "How bytes are counted"). Not consensus: it describes the counting,
	// for tools that size answer ceilings from the limits above.
	JSONPerCountedByteX10 = 33
)

// RelayMsgTypeURLs are the relay msgs: the msgs a relayer submits to deliver
// packets, acknowledgements and timeouts, and the client updates and
// misbehaviour it batches with them. A tx made only of these has no byte cap,
// only gas (see the package doc).
var RelayMsgTypeURLs = []string{
	"/ibc.core.client.v1.MsgUpdateClient",
	"/ibc.core.client.v1.MsgSubmitMisbehaviour",
	"/ibc.core.channel.v1.MsgRecvPacket",
	"/ibc.core.channel.v1.MsgAcknowledgement",
	"/ibc.core.channel.v1.MsgTimeout",
	"/ibc.core.channel.v1.MsgTimeoutOnClose",
	"/ibc.core.channel.v2.MsgRecvPacket",
	"/ibc.core.channel.v2.MsgAcknowledgement",
	"/ibc.core.channel.v2.MsgTimeout",
}

// IsRelayMsg reports whether typeURL is one of RelayMsgTypeURLs.
func IsRelayMsg(typeURL string) bool {
	for _, u := range RelayMsgTypeURLs {
		if u == typeURL {
			return true
		}
	}
	return false
}

// MaxRelayTxResultBytes is the largest msg-phase result a relay tx with the
// given gas limit can store: the free tier plus what its gas pays for.
func MaxRelayTxResultBytes(gasLimit uint64) uint64 {
	return FreeBytes + gasLimit/GasPerByte
}
