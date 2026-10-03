package app

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// AUDIT3-B (F2): a private MsgSend unshield could name x/shieldedstaking's
// (unblocked) module account as its receiver, putting unbooked uerth there:
// invariant 1 broken for good and every later export refused at import.
// checkUnshield now refuses any module account receiver.
func TestAudit3UnshieldIntoStakingModuleRefused(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	requireOK(t, e.finalize(e.sendTx(s, shieldedtest.Send2)).TxResults[0])
	requireOK(t, e.finalize(e.sendTx(s, shieldedtest.Multi3)).TxResults[0])

	ctx := e.ctx()
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
	staking := authtypes.NewModuleAddress(sstypes.ModuleName)
	before := e.app.BankKeeper.GetBalance(ctx, staking, "uerth").Amount
	s.Sends[shieldedtest.Unshield2].Receiver = staking
	m, err := s.Msg(shieldedtest.Unshield2, e.bech(staking),
		shieldedtypes.TxFields{GasLimit: shGas(2)}, func(toml string, pub [][]byte) []byte {
			return e.prover.Prove(t, toml, pub)
		})
	require.NoError(t, err)
	tx := e.privateTx(shGas(2), nil, m)
	ct := e.checkTx(tx)
	require.NotZero(t, ct.Code)
	require.Contains(t, ct.Log, "module account")
	fb := e.finalize(tx)
	require.NotZero(t, fb.TxResults[0].Code)
	ctx = e.ctx()
	require.True(t, before.Equal(e.app.BankKeeper.GetBalance(ctx, staking, "uerth").Amount))
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
}

// AUDIT3-B: private staking's send restriction accepted any transfer from
// the shielded pool. It now accepts pool -> module only inside x/shielded's
// ReleaseToModule (marked context), the path its own private msgs pay by.
func TestAudit3PoolToStakingModuleNeedsRelease(t *testing.T) {
	e := initStakeEnv(t)
	ctx := e.ctx()
	pool := authtypes.NewModuleAddress(shieldedtypes.ModuleName)
	mod := authtypes.NewModuleAddress(sstypes.ModuleName)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
	one := sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))

	_, err := e.app.ShieldedStakingKeeper.SendRestriction(ctx, pool, mod, one)
	require.ErrorIs(t, err, sstypes.ErrSendRestricted)
	_, err = e.app.ShieldedStakingKeeper.SendRestriction(shieldedtypes.WithModuleRelease(ctx, "dex"), pool, mod, one)
	require.ErrorIs(t, err, sstypes.ErrSendRestricted, "a release to another module does not count")
	_, err = e.app.ShieldedStakingKeeper.SendRestriction(shieldedtypes.WithModuleRelease(ctx, sstypes.ModuleName), pool, mod, one)
	require.NoError(t, err)

	e.auditFundPool(10)
	ctx = e.ctx()
	require.ErrorIs(t, e.app.BankKeeper.SendCoins(ctx, pool, mod, one), sstypes.ErrSendRestricted)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
}
