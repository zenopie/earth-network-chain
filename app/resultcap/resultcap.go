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
// Bytes are counted as the proto size of a msg's events and responses plus
// EventOverhead, AttributeOverhead and MsgOverhead, which cover what baseapp
// adds when it stores them (msg_index attributes, index flags, the "message"
// event). The ante phase is not metered (a signed tx's ante events are a
// fixed handful, under 1 KB; a private tx's are a function of its shape,
// 6.5 KB at most, and capped per block by max_private_actions_per_block).
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
// # Worst cases
//
// Stored bytes (proto ExecTxResult), at genesis block max_gas 100M, as
// measured by app's TestResultCapWorstCase with every attribute filled with
// '<' (CometBFT's and the SDK's JSON write it as the 6-byte \u003c, so JSON
// answers are up to ~6x the stored bytes):
//
//   - Any tx with a non-relay msg: MaxTxResultBytes of msg results plus its
//     ante events: 1,048,712 B stored; RPC tx ~6.3 MB of JSON.
//   - One relay msg: core logs a packet's data hex-encoded twice (~4x its
//     size), plus at most MaxPacketAppResultBytes from the application. An
//     ICS-20 packet with 32 KiB of '<' as its memo has ~197 KB of data (its
//     JSON escapes '<' too): 823 KB stored without a callback, ~1.2 MB with
//     the largest one. Any relay msg is also within the relay tx bound.
//   - A relay tx: MaxRelayTxResultBytes(gas_limit), 5,008,192 B at 100M gas,
//     plus its ante events. Measured: 16 packets whose callbacks emit 252 KiB
//     of '<' each store 4.2 MB at 93.6M gas; RPC tx ~24.9 MB of JSON.
//   - A block: paid bytes and free tiers share block gas. A free-tier tx
//     (8 KiB of '<' from a contract) costs ~136k gas, so ~740 of them fill a
//     block: ~6.5 MB stored, ~35.5 MB of block_results JSON; all-paid is at
//     most 5 MB stored. Gov's EndBlock adds at most MaxEndBlockResultBytes.
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
	// MaxCallbackResultBytes plus an ICS-20 packet's own events (~35 KB with
	// the largest memo), so an ICS-20 receive within the callback cap never
	// trips it.
	MaxPacketAppResultBytes = 384 << 10 // 384 KiB

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
