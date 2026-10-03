package app

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	earthtypes "github.com/earth-network/earth/x/earth/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// Re-audit R3: an operator whose account is a vesting account would have the
// epoch's compounding delegate coins it was just paid, which x/bank tracks
// against its vesting coins first: every epoch its spendable balance would
// grow by the compounded amount (rewards unlocking vesting). Such an
// account cannot create a validator.
func TestVestingAccountCannotOperateValidator(t *testing.T) {
	e := initStakeEnv(t)
	key := secp256k1.GenPrivKeyFromSecret([]byte("reaudit-vest-op"))
	op := sdk.AccAddress(key.PubKey().Address())
	ctx := e.ctx()
	self := int64(1000 * ssErth)
	locked := int64(5000 * ssErth)
	liquid := int64(10 * ssErth)
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", self+locked+liquid))
	base := authtypes.NewBaseAccountWithAddress(op)
	base.AccountNumber = e.app.AuthKeeper.NextAccountNumber(ctx)
	vacc, err := vestingtypes.NewContinuousVestingAccount(base, sdk.NewCoins(sdk.NewInt64Coin("uerth", self+locked)),
		ctx.BlockTime().Unix(), ctx.BlockTime().Add(100*365*24*time.Hour).Unix())
	require.NoError(t, err)
	e.app.AuthKeeper.SetAccount(ctx, vacc)
	require.NoError(t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, op, coins))
	val := sdk.ValAddress(op)
	msg, err := stakingtypes.NewMsgCreateValidator(e.valoper(val), ed25519.GenPrivKeyFromSecret([]byte("reaudit-cons")).PubKey(),
		sdk.NewInt64Coin("uerth", self), stakingtypes.Description{Moniker: "vest"},
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	cc, _ := ctx.CacheContext()
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).CreateValidator(cc, msg)
	require.ErrorIs(t, err, sstypes.ErrVestingOperator)
}

// The compounding guard: were an operator's account to become a vesting
// account anyway (here forced in state, past the hook), the epoch end refuses
// to compound rather than let the delegation unlock vesting coins: its
// spendable balance does not move.
func TestCompoundingNeverMovesOperatorSpendable(t *testing.T) {
	e := initStakeEnv(t)
	key := secp256k1.GenPrivKeyFromSecret([]byte("reaudit-vest-op2"))
	op := sdk.AccAddress(key.PubKey().Address())
	ctx := e.ctx()
	self := int64(1000 * ssErth)
	locked := int64(5000 * ssErth)
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", self+locked+10*ssErth))
	require.NoError(t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, op, coins))
	val := sdk.ValAddress(op)
	msg, err := stakingtypes.NewMsgCreateValidator(e.valoper(val), ed25519.GenPrivKeyFromSecret([]byte("reaudit-cons2")).PubKey(),
		sdk.NewInt64Coin("uerth", self), stakingtypes.Description{Moniker: "vest2"},
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).CreateValidator(ctx, msg)
	require.NoError(t, err)
	// Forced: the operator's account becomes a vesting account.
	base, ok := e.app.AuthKeeper.GetAccount(ctx, op).(*authtypes.BaseAccount)
	require.True(t, ok)
	// Delayed: nothing vests with time, so only a delegation could move it.
	vacc, err := vestingtypes.NewDelayedVestingAccount(base, sdk.NewCoins(sdk.NewInt64Coin("uerth", locked)),
		ctx.BlockTime().Add(100*365*24*time.Hour).Unix())
	require.NoError(t, err)
	e.app.AuthKeeper.SetAccount(ctx, vacc)
	e.next(5 * time.Second)
	e.next(time.Hour)

	spend := func() math.Int { return e.app.BankKeeper.SpendableCoins(e.ctx(), op).AmountOf("uerth") }
	s0 := spend()
	for i := 0; i < 3; i++ {
		r := e.next(24 * time.Hour)
		for _, ev := range eventsOf(r.Events, sstypes.EventTypeSelfBond) {
			require.NotEqual(t, e.valoper(val), ev["validator"], "compounded a vesting operator")
		}
	}
	require.True(t, spend().Equal(s0), "spendable moved: %s -> %s", s0, spend())
}
