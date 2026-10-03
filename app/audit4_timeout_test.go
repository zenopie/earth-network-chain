package app

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// Audit 4 L1: a private tx whose timeout_height is at or below the last
// committed height can only fail in the next block. CheckTx refuses it (the
// SDK's rule admitted timeout == height), and PrepareProposal leaves out a
// tx whose timeout is below the proposal's height before counting it toward
// the private action cap.
func TestAudit4ExpiredTimeoutHeight(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	requireOK(t, e.shieldAll(s).TxResults[0])
	m := e.sendMsg(s, shieldedtest.Send2)
	withTimeout := func(h uint64) []byte {
		return e.privateTxFields(shieldedtypes.TxFields{TimeoutHeight: h, GasLimit: shGas(2)}, m)
	}

	expired := withTimeout(uint64(e.height))
	res := e.checkTx(expired)
	require.Equal(t, sdkerrors.ErrTxTimeoutHeight.ABCICode(), res.Code, res.Log)
	// One past the committed height is not refused for its timeout (it fails
	// later: the proof was made for other tx fields).
	res = e.checkTx(withTimeout(uint64(e.height) + 1))
	require.NotEqual(t, sdkerrors.ErrTxTimeoutHeight.ABCICode(), res.Code, res.Log)

	live := e.sendTx(s, shieldedtest.Send2)
	resp, err := e.app.PrepareProposal(&abci.RequestPrepareProposal{
		Txs: [][]byte{expired, live, withTimeout(uint64(e.height) + 1)}, MaxTxBytes: 1 << 30, Height: e.height + 1, Time: e.now,
	})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 2, "the expired tx is left out")
	require.Equal(t, live, resp.Txs[0])
}
