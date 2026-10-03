package app

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	earthtypes "github.com/earth-network/earth/x/earth/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// AUDIT3-A: anyone can make a future validator's reward escrow a permanently
// locked vesting account (1 uerth) before MsgCreateValidator. Only the
// escrow's spendable coins move, so the escrow still compounds every epoch
// and is released on retirement; the locked uerth stays.
func TestAudit3EscrowPoisonedByLockedAccount(t *testing.T) {
	e := initStakeEnv(t)
	key := secp256k1.GenPrivKeyFromSecret([]byte("audit3/victim-op"))
	op := sdk.AccAddress(key.PubKey().Address())
	val := sdk.ValAddress(op)
	escrow := sstypes.RewardEscrowAddress(val)

	poison := vestingtypes.NewMsgCreatePermanentLockedAccount(e.userAddr(), escrow, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1)))
	r := e.run(e.signedTx(e.user, 300_000, 5_000, poison))
	require.Equal(t, uint32(0), r.Code, r.Log)

	// Creating the validator is not refused (that would let anyone block any
	// future validator).
	ctx := e.ctx()
	self := int64(1000 * ssErth)
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", self+ssErth))
	require.NoError(t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, op, coins))
	msg, err := stakingtypes.NewMsgCreateValidator(e.valoper(val), ed25519.GenPrivKeyFromSecret([]byte("audit3/cons")).PubKey(),
		sdk.NewInt64Coin("uerth", self), stakingtypes.Description{Moniker: "victim"},
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).CreateValidator(ctx, msg)
	require.NoError(t, err)

	compounded, failures := 0, 0
	e.next(5 * time.Second)
	for i := 0; i < 4; i++ {
		res := e.next(24 * time.Hour)
		for _, ev := range eventsOf(res.Events, sstypes.EventTypeSelfBond) {
			if ev["validator"] == e.valoper(val) {
				compounded++
			}
		}
		for _, ev := range eventsOf(res.Events, sstypes.EventTypeEpochFailure) {
			if ev["validator"] == e.valoper(val) {
				failures++
				t.Logf("epoch failure: stage=%s err=%s", ev["stage"], ev["error"])
			}
		}
	}
	require.Positive(t, compounded, "the poisoned escrow still compounds")
	require.Zero(t, failures)
	require.Equal(t, int64(1), e.app.BankKeeper.GetBalance(e.ctx(), escrow, "uerth").Amount.Int64(), "only the locked coin stays")

	// Retirement: the release pays the spendable balance and the entry goes.
	ctx = e.ctx()
	d, err := e.app.StakingKeeper.GetDelegation(ctx, op, val)
	require.NoError(t, err)
	v, err := e.app.StakingKeeper.GetValidator(ctx, val)
	require.NoError(t, err)
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).Undelegate(ctx, stakingtypes.NewMsgUndelegate(
		e.bech(op), e.valoper(val), sdk.NewCoin("uerth", v.TokensFromShares(d.Shares).TruncateInt())))
	require.NoError(t, err)
	stuck := 0
	for i := 0; i < 24; i++ {
		res := e.next(24 * time.Hour)
		for _, ev := range eventsOf(res.Events, sstypes.EventTypeEpochFailure) {
			if ev["stage"] == "escrow_retire" || ev["stage"] == "escrow_release" {
				stuck++
			}
		}
	}
	e.next(5 * time.Second)
	require.Zero(t, stuck)
	has := false
	_ = e.app.ShieldedStakingKeeper.RetiringEscrows.Walk(e.ctx(), nil, func(k collections.Pair[int64, []byte]) (bool, error) {
		if sdk.ValAddress(k.K2()).Equals(val) {
			has = true
		}
		return false, nil
	})
	require.False(t, has, "retirement entry released")
	require.Equal(t, int64(1), e.app.BankKeeper.GetBalance(e.ctx(), escrow, "uerth").Amount.Int64())
}

// failingEscrow sets up a validator address whose escrow release always
// fails (its operator account is itself a recorded escrow, which takes coins
// only from distribution), with 1 spendable uerth in its escrow.
func (e *stakeEnv) audit3Escrow(prefix byte, i int, failing bool) sdk.ValAddress {
	ctx := e.ctx()
	b := make([]byte, 20)
	b[0], b[1] = prefix, byte(i)
	val := sdk.ValAddress(b)
	escrow := sstypes.RewardEscrowAddress(val)
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))
	require.NoError(e.t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(e.t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, escrow, coins))
	require.NoError(e.t, e.app.ShieldedStakingKeeper.RewardEscrows.Set(ctx, escrow, val))
	if failing {
		require.NoError(e.t, e.app.ShieldedStakingKeeper.RewardEscrows.Set(ctx, sdk.AccAddress(val), sdk.ValAddress(escrow)))
	}
	return val
}

// AUDIT3-A: escrow releases that keep failing cannot starve the per-block
// (retirement) or per-epoch (pending release) budgets: failed entries rotate.
func TestAudit3FailingEscrowReleasesDoNotStarveQueues(t *testing.T) {
	e := initStakeEnv(t)
	n := sstypes.EscrowRetireLimit + 10
	now := e.ctx().BlockTime().UnixNano()
	for i := 0; i < n; i++ {
		require.NoError(t, e.app.ShieldedStakingKeeper.PendingReleases.Set(e.ctx(), e.audit3Escrow(0x01, i, true)))
		require.NoError(t, e.app.ShieldedStakingKeeper.RetiringEscrows.Set(e.ctx(),
			collections.Join(now, []byte(e.audit3Escrow(0x02, i, true)))))
	}
	goodPending := e.audit3Escrow(0xfe, 0, false)
	require.NoError(t, e.app.ShieldedStakingKeeper.PendingReleases.Set(e.ctx(), goodPending))
	goodRetire := e.audit3Escrow(0xff, 0, false)
	require.NoError(t, e.app.ShieldedStakingKeeper.RetiringEscrows.Set(e.ctx(), collections.Join(now+1, []byte(goodRetire))))

	// Retirements: one block moves the failing head back; the next pays.
	e.next(time.Second)
	e.next(time.Second)
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), sstypes.RewardEscrowAddress(goodRetire), "uerth").IsZero(),
		"a due retirement behind failing entries is released")

	// Pending releases: two epoch ends reach the good entry.
	e.next(24 * time.Hour)
	e.next(24 * time.Hour)
	has, err := e.app.ShieldedStakingKeeper.PendingReleases.Has(e.ctx(), goodPending)
	require.NoError(t, err)
	require.False(t, has, "a pending release behind failing entries is retried")
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), sstypes.RewardEscrowAddress(goodPending), "uerth").IsZero())
}
