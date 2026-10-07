package networks

import (
	"bytes"
	"strconv"
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
)

// mempoolMaxTxBytes is CometBFT's default [mempool] max_tx_bytes: the largest
// tx a node with a stock config.toml admits, and so the largest tx an honest
// proposer includes.
const mempoolMaxTxBytes = 1 << 20

// blockOverhead is room for the header, the last commit and the tx framing
// (CometBFT's MaxOverheadForBlock, MaxHeaderBytes and a commit of a few
// hundred validators come to well under this).
const blockOverhead = 128 << 10

// txEnvelope is room for what wraps a msg in a tx: body, auth info, fee,
// signature.
const txEnvelope = 1 << 10

// maxBundle is the largest bundle the chain's shape checks admit: the
// x/shielded ceiling of orchard.MaxActions actions (genesis allows 16), each
// at its maximum field sizes.
func maxBundle(actions int) shieldedtypes.Bundle {
	b := shieldedtypes.Bundle{BindingSig: make([]byte, 64),
		Balances: []shieldedtypes.ValueBalance{{Denom: "uerth", Amount: ^uint64(0)}, {Denom: "uanml", Amount: ^uint64(0)}}}
	for i := 0; i < actions; i++ {
		b.Actions = append(b.Actions, shieldedtypes.Action{
			Anchor: make([]byte, 32), Nullifier: make([]byte, 32), Commitment: make([]byte, 32), Cv: make([]byte, 32),
			Ciphertext: make([]byte, shieldedtypes.MaxCiphertextBytes), Proof: make([]byte, shieldedtypes.ProofBytes),
		})
	}
	return b
}

// TestBlockSizeLimits: the consensus block limits are chain.json's, and the
// byte limit fits the largest tx each path can carry, with evidence, at a
// size that keeps a block's RPC answer small (chain.json's comment on
// block_max_bytes has the reasoning).
func TestBlockSizeLimits(t *testing.T) {
	g := readJSON[struct {
		Consensus struct {
			Params struct {
				Block struct {
					MaxBytes string `json:"max_bytes"`
					MaxGas   string `json:"max_gas"`
				} `json:"block"`
				Evidence struct {
					MaxBytes string `json:"max_bytes"`
				} `json:"evidence"`
			} `json:"params"`
		} `json:"consensus"`
	}](t, "genesis.json")
	c := readJSON[struct {
		BlockMaxGas   string `json:"block_max_gas"`
		BlockMaxBytes string `json:"block_max_bytes"`
	}](t, "genesis/chain.json")

	p := g.Consensus.Params
	if p.Block.MaxBytes != c.BlockMaxBytes || p.Block.MaxGas != c.BlockMaxGas {
		t.Fatalf("block max_bytes/max_gas %s/%s in genesis.json, %s/%s in chain.json: run scripts/build-genesis.sh",
			p.Block.MaxBytes, p.Block.MaxGas, c.BlockMaxBytes, c.BlockMaxGas)
	}
	maxBytes, err := strconv.Atoi(p.Block.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := strconv.Atoi(p.Evidence.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}

	send := shieldedtypes.MsgSend{Bundle: maxBundle(orchard.MaxActions), Receiver: "earth1" + string(bytes.Repeat([]byte("q"), 58)), Fee: ^uint64(0)}
	signals := make([]string, personhoodtypes.MaxPublicSignals)
	for i := range signals {
		signals[i] = string(bytes.Repeat([]byte("9"), 78)) // a field element in decimal
	}
	reg := personhoodtypes.MsgRegister{
		Fee: maxBundle(orchard.MaxActions), Proof: make([]byte, shieldedtypes.ProofBytes), PublicSignals: signals,
		SignatureAlgorithm: string(bytes.Repeat([]byte("x"), 64)), DscDer: make([]byte, personhoodtypes.MaxDscDerBytes),
		Idc: make([]byte, 32), PcAnml: make([]byte, 32), CiphertextAnml: make([]byte, 177), PcErth: make([]byte, 32),
		CiphertextErth: make([]byte, 177), AffiliateHandle: string(bytes.Repeat([]byte("h"), 64)),
	}
	store := wasmtypes.MsgStoreCode{Sender: send.Receiver, WASMByteCode: make([]byte, wasmtypes.MaxWasmSize)}

	for _, tc := range []struct {
		what  string
		bytes int
	}{
		// One private tx carries one bundle (MaxBundlesPerMsg) plus at most
		// one proof of its own (membership, stake, vote, move).
		{"the largest private tx", send.Size() + shieldedtypes.ProofBytes + txEnvelope},
		{"the largest registration", reg.Size() + txEnvelope},
		{"a contract upload at MaxWasmSize", store.Size() + txEnvelope},
		{"the largest tx a default mempool admits", mempoolMaxTxBytes},
	} {
		t.Logf("%-40s %8d bytes", tc.what, tc.bytes)
		if tc.bytes+evidence+blockOverhead > maxBytes {
			t.Errorf("%s (%d bytes) and a block's evidence (%d) do not fit block max_bytes %d", tc.what, tc.bytes, evidence, maxBytes)
		}
	}
	// A gov proposal may store code up to wasmd's MaxProposalWasmSize, if the
	// nodes admit a tx that large: it must at least fit a block without evidence.
	if n := wasmtypes.MaxProposalWasmSize + txEnvelope; n+blockOverhead > maxBytes {
		t.Errorf("a gov StoreCode at MaxProposalWasmSize (%d bytes) does not fit block max_bytes %d", n, maxBytes)
	}
	// The default private-action cap's worth of actions fits with room over.
	if n := int(shieldedtypes.DefaultMaxPrivateActionsPerBlock) * maxBundle(1).Actions[0].Size(); 4*n > maxBytes {
		t.Errorf("a block's %d private actions (%d bytes) take over a quarter of max_bytes %d",
			shieldedtypes.DefaultMaxPrivateActionsPerBlock, n, maxBytes)
	}
	// The upper side: every node holds and serves blocks of this size, and
	// public `block` answers are sized by it (base64 in JSON). CometBFT's
	// default is 22 MiB; this chain needs a fraction of that.
	if maxBytes > 8<<20 {
		t.Errorf("block max_bytes %d is over 8 MiB: no tx needs it, and every block read and gossip buffer is sized by it", maxBytes)
	}
}
