package app

import (
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
)

// AUDIT3 F1: a validator whose app.toml sets mempool.max-txs >= 0 used to run
// a SenderNonceMempool, whose Remove refuses a tx with no signer after the
// ante wrote: the private tx failed there and ran everywhere else (a fork).
// app.New now forces the no-op mempool as its last baseapp option, so the
// same configuration computes the same block result.
func TestAudit3AppMempoolForcedNoOp(t *testing.T) {
	gt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	noop := initShieldedEnvWith(t, shieldedEnvOpts{genesisTime: gt, keySeed: "audit3"})
	sn := initShieldedEnvWith(t, shieldedEnvOpts{genesisTime: gt, keySeed: "audit3",
		baseOpts: []func(*baseapp.BaseApp){baseapp.SetMempool(mempool.NewSenderNonceMempool(mempool.SenderNonceMaxTxOpt(5000)))}})
	require.IsType(t, mempool.NoOpMempool{}, sn.app.Mempool())
	s := shieldedtest.Default()
	requireOK(t, noop.shieldAll(s).TxResults[0])
	requireOK(t, sn.shieldAll(s).TxResults[0])

	tx := noop.sendTx(s, shieldedtest.Send2)
	ct := sn.checkTx(tx)
	require.Zero(t, ct.Code, ct.Log)
	ra := noop.finalize(tx)
	rb := sn.finalize(tx)
	requireOK(t, ra.TxResults[0])
	requireOK(t, rb.TxResults[0])
	require.Equal(t, ra.TxResults[0].GasUsed, rb.TxResults[0].GasUsed)
	require.Equal(t, len(ra.TxResults[0].Events), len(rb.TxResults[0].Events))
}

// AUDIT3 F5: a proposal left the block's private action cap to the ante, so
// a proposer could fill a block with private txs certain to fail (ErrBlockCap)
// and drop them from every mempool. PrepareProposal now leaves out a private
// tx past the cap (it stays in the mempool for a later block).
func TestAudit3PrepareProposalRespectsPrivateActionCap(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	requireOK(t, e.shieldAll(s).TxResults[0])
	tx := e.sendTx(s, shieldedtest.Send2)
	n, ok := privateActions(e.app.TxConfig().TxDecoder()(tx))
	require.True(t, ok)
	params, err := e.app.ShieldedKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	limit := uint64(params.MaxPrivateActionsPerBlock)
	fit := int(limit / n)

	var txs [][]byte
	for i := 0; i < fit+3; i++ {
		txs = append(txs, tx)
	}
	resp, err := e.app.PrepareProposal(&abci.RequestPrepareProposal{
		Txs: txs, MaxTxBytes: 1 << 30, Height: e.height + 1, Time: e.now,
	})
	require.NoError(t, err)
	require.Equal(t, fit, len(resp.Txs), "%d actions per tx, cap %d", n, limit)
}
