package app

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
)

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
