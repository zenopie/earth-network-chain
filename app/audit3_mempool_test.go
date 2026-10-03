package app

import (
	"testing"
	"time"

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
