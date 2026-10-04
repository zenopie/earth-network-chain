package app

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"
)

// D7-L1: a send to the gov module account is refused (it is on the blocked
// list), so its balance stays exactly the deposits x/gov's InitGenesis
// requires; deposits still reach it and a proposal still pays them back.
func TestGovAccountBlocked(t *testing.T) {
	e := initStakeEnv(t)
	gov := authtypes.NewModuleAddress(govtypes.ModuleName)
	require.True(t, e.app.BankKeeper.BlockedAddr(gov))
	send := &banktypes.MsgSend{FromAddress: e.bech(e.userAddr()), ToAddress: e.bech(gov),
		Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))}
	res := e.run(e.signedTx(e.user, 200_000, 5_000, send))
	require.NotEqual(t, uint32(0), res.Code, "a send to gov is refused")
	require.Contains(t, res.Log, "not allowed to receive funds")

	// A deposit still lands, and the account holds exactly the deposits.
	before := e.app.BankKeeper.GetBalance(e.ctx(), gov, "uerth").Amount
	prop := e.submitProposal()
	e.next(5 * time.Second)
	after := e.app.BankKeeper.GetBalance(e.ctx(), gov, "uerth").Amount
	require.Equal(t, before.Add(math.NewInt(2*ssErth)), after)
	p, err := e.app.GovKeeper.Proposals.Get(e.ctx(), prop)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, p.Status)
}
