package app

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// Audit 6 A-L1: a private tx's gas_limit is the block space it claims, and
// its gas use is known by the end of the private ante; a limit past
// PrivateGasCeilingFactor x the use is refused, before anything else is
// checked, in CheckTx and in a block. Within it, the tx goes on to its
// checks (here its binding signature, which binds the limit it was proven
// for).
func TestAudit6PrivateGasLimitBounded(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	tx := shieldedtypes.TxFields{GasLimit: shGas(2)}
	m := e.sendMsgFor(s, shieldedtest.Send2, tx)

	huge := e.privateTxFields(shieldedtypes.TxFields{GasLimit: 100_000_000}, m)
	res := e.checkTx(huge)
	require.Equal(t, sdkerrors.ErrInvalidRequest.ABCICode(), res.Code, res.Log)
	require.Contains(t, res.Log, "exceeds what this private tx uses")
	fb := e.finalize(huge)
	require.Contains(t, fb.TxResults[0].Log, "exceeds what this private tx uses")

	within := e.privateTxFields(shieldedtypes.TxFields{GasLimit: tx.GasLimit + 1_000}, m)
	res = e.checkTx(within)
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), res.Code, res.Log)

	good := e.privateTxFields(tx, m)
	require.Equal(t, uint32(0), e.checkTx(good).Code)
	requireOK(t, e.finalize(good).TxResults[0])
}
