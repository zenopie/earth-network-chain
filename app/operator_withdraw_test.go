package app

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// A validator operator's self-bond always compounds, so its rewards always
// land in the operator account. Genesis disables withdraw addresses for
// everyone (every route); with them re-enabled (a governance flip), an
// operator is still refused — in the ante, top level and inside authz
// MsgExec — while other accounts are not; an account with a foreign
// withdraw address cannot become an operator; and a foreign address that
// arrived by a route nothing refuses is reset at epoch end, and the
// self-bond compounds rather than being skipped.
func TestOperatorWithdrawAddrRefused(t *testing.T) {
	e := initStakeEnv(t)
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	opB := sdk.AccAddress(vB)
	user := e.userAddr()
	setWA := func(del, wa sdk.AccAddress) *distrtypes.MsgSetWithdrawAddress {
		return distrtypes.NewMsgSetWithdrawAddress(del, wa)
	}

	// --- genesis: disabled for every account and route.
	p, err := e.app.DistrKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	require.False(t, p.WithdrawAddrEnabled)
	// The ante and the app's router refuse it with this chain's error;
	// x/distribution itself (baseapp's router, past the ante) with its own.
	fb := e.run(e.signedTx(e.user, 300_000, 5_000, setWA(user, opB)))
	require.Equal(t, sstypes.ErrOperatorWithdraw.ABCICode(), fb.Code, fb.Log)
	require.Contains(t, fb.Log, "withdraw addresses cannot be changed")
	for _, m := range []sdk.Msg{setWA(user, opB), setWA(opB, user), setWA(user, user)} {
		cc, _ := e.ctx().CacheContext()
		_, err := e.app.MsgServiceRouter().Handler(m)(cc, m)
		require.ErrorIs(t, err, distrtypes.ErrSetWithdrawAddrDisabled)
		cc, _ = e.ctx().CacheContext()
		_, err = e.app.rewardsRouter.Handler(m)(cc, m)
		require.ErrorIs(t, err, sstypes.ErrOperatorWithdraw)
	}

	// --- governance re-enables withdraw addresses.
	p.WithdrawAddrEnabled = true
	require.NoError(t, e.app.DistrKeeper.Params.Set(e.ctx(), p))

	// An operator: refused in the ante, top level and through authz.
	res := e.checkTx(e.signedTx(vBKey, 300_000, 5_000, setWA(opB, user)))
	require.Equal(t, sstypes.ErrOperatorWithdraw.ABCICode(), res.Code, res.Log)
	require.NoError(t, e.app.AuthzKeeper.SaveGrant(e.ctx(), user, opB,
		authz.NewGenericAuthorization(sdk.MsgTypeURL(&distrtypes.MsgSetWithdrawAddress{})), nil))
	exec := authz.NewMsgExec(user, []sdk.Msg{setWA(opB, user)})
	res = e.checkTx(e.signedTx(e.user, 300_000, 5_000, &exec))
	require.Equal(t, sstypes.ErrOperatorWithdraw.ABCICode(), res.Code, res.Log)
	// To itself is no change, and passes.
	fb = e.run(e.signedTx(vBKey, 300_000, 5_000, setWA(opB, opB)))
	require.Equal(t, uint32(0), fb.Code, fb.Log)

	// Any other account may.
	fb = e.run(e.signedTx(e.user, 300_000, 5_000, setWA(user, opB)))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	wa, err := e.app.DistrKeeper.GetDelegatorWithdrawAddr(e.ctx(), user)
	require.NoError(t, err)
	require.Equal(t, opB, wa)

	// ...but then cannot become an operator (the staking hook, any route).
	ctx, _ := e.ctx().CacheContext()
	msg, err := stakingtypes.NewMsgCreateValidator(e.valoper(sdk.ValAddress(user)),
		ed25519.GenPrivKeyFromSecret([]byte("withdraw-test/cons")).PubKey(), sdk.NewInt64Coin("uerth", 10*ssErth),
		stakingtypes.Description{Moniker: "user"},
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).CreateValidator(ctx, msg)
	require.ErrorIs(t, err, sstypes.ErrOperatorWithdraw)

	// --- a foreign address that arrived by a route nothing refuses: reset at
	// epoch end, and the self-bond compounds.
	require.NoError(t, e.app.DistrKeeper.SetDelegatorWithdrawAddr(e.ctx(), opB, user))
	var end *struct{ compounded, reset bool }
	for i := 0; i < 3 && end == nil; i++ {
		r := e.next(24 * time.Hour)
		if len(eventsOf(r.Events, sstypes.EventTypeEpoch)) == 0 {
			continue
		}
		end = &struct{ compounded, reset bool }{}
		for _, ev := range eventsOf(r.Events, sstypes.EventTypeSelfBond) {
			if ev["validator"] == e.valoper(vB) {
				end.compounded = true
			}
		}
		for _, ev := range eventsOf(r.Events, sstypes.EventTypeWithdrawAddrReset) {
			require.Equal(t, e.valoper(vB), ev["validator"])
			require.Equal(t, e.bech(user), ev["withdraw_address"])
			end.reset = true
		}
	}
	require.NotNil(t, end, "no epoch end")
	require.True(t, end.reset, "foreign withdraw address reset")
	require.True(t, end.compounded, "operator compounded, not skipped")
	wa, err = e.app.DistrKeeper.GetDelegatorWithdrawAddr(e.ctx(), opB)
	require.NoError(t, err)
	require.Equal(t, opB, wa)
	e.invariants()
}

// A genesis in which a validator's operator has a withdraw address elsewhere
// is refused: by `genesis validate` (app.ValidateOperatorWithdrawAddrs, for
// gentxs and staking validators alike) and by InitChain.
func TestGenesisOperatorWithdrawAddr(t *testing.T) {
	other := sdk.AccAddress([]byte("some-other-account01"))
	withInfo := func(appState map[string]json.RawMessage, op sdk.AccAddress) {
		var d map[string]any
		require.NoError(t, json.Unmarshal(appState["distribution"], &d))
		d["delegator_withdraw_infos"] = []any{map[string]any{
			"delegator_address": op.String(), "withdraw_address": other.String(),
		}}
		bz, err := json.Marshal(d)
		require.NoError(t, err)
		appState["distribution"] = bz
	}

	var state map[string]json.RawMessage
	var valOp sdk.AccAddress
	// InitChain refuses it: the gentx's MsgCreateValidator fails in the
	// staking hook, and genutil panics on a failed gentx.
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		_, err = initStakeEnvWith(t, func(appState map[string]json.RawMessage, op sdk.AccAddress) {
			withInfo(appState, op)
			state, valOp = appState, op
		})
		return err
	}()
	require.Error(t, err, "InitChain refuses it")
	require.Contains(t, err.Error(), sstypes.ErrOperatorWithdraw.Error())

	a := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()})
	dec := a.TxConfig().TxJSONDecoder()
	err = ValidateOperatorWithdrawAddrs(a.AppCodec(), dec, state)
	require.ErrorContains(t, err, "gentx 0")

	// A staking genesis validator rather than a gentx.
	noGentx := map[string]json.RawMessage{"distribution": state["distribution"]}
	st := stakingtypes.DefaultGenesisState()
	st.Validators = []stakingtypes.Validator{{OperatorAddress: sdk.ValAddress(valOp).String()}}
	noGentx["staking"], err = a.AppCodec().MarshalJSON(st)
	require.NoError(t, err)
	require.ErrorContains(t, ValidateOperatorWithdrawAddrs(a.AppCodec(), dec, noGentx), "validator operator")

	// To itself, or for a non-operator: fine.
	var d distrtypes.GenesisState
	require.NoError(t, a.AppCodec().UnmarshalJSON(state["distribution"], &d))
	d.DelegatorWithdrawInfos = []distrtypes.DelegatorWithdrawInfo{
		{DelegatorAddress: valOp.String(), WithdrawAddress: valOp.String()},
		{DelegatorAddress: other.String(), WithdrawAddress: valOp.String()},
	}
	state["distribution"], err = a.AppCodec().MarshalJSON(&d)
	require.NoError(t, err)
	require.NoError(t, ValidateOperatorWithdrawAddrs(a.AppCodec(), dec, state))
}

// A validator's self-bond rewards and commission compound at the epoch end
// and cannot be claimed: MsgWithdrawDelegatorReward from an operator and
// every MsgWithdrawValidatorCommission are refused in the ante (top level and
// inside authz MsgExec) and by the app's message router, which authz
// dispatch, gov and group execution (depinject binding), the ICA host and
// contracts use. A non-operator's claim is not refused by the filter.
func TestOperatorRewardClaimRefused(t *testing.T) {
	e := initStakeEnv(t)
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.next(time.Hour)
	opB := sdk.AccAddress(vB)
	user := e.userAddr()
	claims := []sdk.Msg{
		distrtypes.NewMsgWithdrawDelegatorReward(e.bech(opB), e.valoper(vB)),
		distrtypes.NewMsgWithdrawValidatorCommission(e.valoper(vB)),
	}
	code := sstypes.ErrOperatorRewardClaim.ABCICode()

	for _, m := range claims {
		// Top level.
		res := e.checkTx(e.signedTx(vBKey, 300_000, 5_000, m))
		require.Equal(t, code, res.Code, "%T: %s", m, res.Log)

		// Inside authz MsgExec (the ante recurses).
		require.NoError(t, e.app.AuthzKeeper.SaveGrant(e.ctx(), user, opB,
			authz.NewGenericAuthorization(sdk.MsgTypeURL(m)), nil))
		exec := authz.NewMsgExec(user, []sdk.Msg{m})
		res = e.checkTx(e.signedTx(e.user, 300_000, 5_000, &exec))
		require.Equal(t, code, res.Code, "%T: %s", m, res.Log)

		// Past the ante: authz dispatch (its router is the filtering one,
		// bound by depinject, as gov's and group's are) and the router as the
		// ICA host and contracts call it.
		cc, _ := e.ctx().CacheContext()
		_, err := e.app.AuthzKeeper.DispatchActions(cc, user, []sdk.Msg{m})
		require.ErrorIs(t, err, sstypes.ErrOperatorRewardClaim, "%T", m)
		cc, _ = e.ctx().CacheContext()
		_, err = e.app.rewardsRouter.Handler(m)(cc, m)
		require.ErrorIs(t, err, sstypes.ErrOperatorRewardClaim, "%T", m)
	}

	// Nothing was paid: the commission is still accrued.
	c, err := e.app.DistrKeeper.GetValidatorAccumulatedCommission(e.ctx(), vB)
	require.NoError(t, err)
	require.True(t, c.Commission.AmountOf("uerth").IsPositive())

	// A non-operator is not refused by the filter (it holds no delegation,
	// so x/distribution refuses it for its own reason).
	m := distrtypes.NewMsgWithdrawDelegatorReward(e.bech(user), e.valoper(vB))
	cc, _ := e.ctx().CacheContext()
	_, err = e.app.rewardsRouter.Handler(m)(cc, m)
	require.Error(t, err)
	require.NotErrorIs(t, err, sstypes.ErrOperatorRewardClaim)
	e.invariants()
}

// Every SDK msg this chain blocks fails with a registered error whose text
// says why and what to do instead, the same text whether the ante refuses it
// or the staking hook / app router does. An operator's own undelegation
// (the 21-day exit) is not blocked.
func TestBlockedMsgErrors(t *testing.T) {
	e := initStakeEnv(t)
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.next(time.Hour)
	opB := sdk.AccAddress(vB)
	user, valoper := e.bech(e.userAddr()), e.valoper(vB)
	amt := sdk.NewInt64Coin("uerth", ssErth)
	staking := sstypes.ErrTransparentStaking
	const stakingText = "delegation is private on Earth: stake with the Earth Wallet (shielded staking)"
	const claimText = "validator rewards and commission auto-compound into self-bond and cannot be withdrawn"
	const waText = "withdraw addresses cannot be changed"
	for _, c := range []struct {
		signer bool // false: the user signs; true: the operator
		m      sdk.Msg
		err    error
		text   string
	}{
		{false, stakingtypes.NewMsgDelegate(user, valoper, amt), staking, stakingText},
		{false, stakingtypes.NewMsgUndelegate(user, valoper, amt), staking, stakingText},
		{false, stakingtypes.NewMsgCancelUnbondingDelegation(user, valoper, 1, amt), staking, stakingText},
		{true, stakingtypes.NewMsgBeginRedelegate(e.bech(opB), valoper, e.valoper(e.genesisValidator()), amt), staking, stakingText},
		{false, distrtypes.NewMsgSetWithdrawAddress(e.userAddr(), opB), sstypes.ErrOperatorWithdraw, waText},
		{true, distrtypes.NewMsgSetWithdrawAddress(opB, e.userAddr()), sstypes.ErrOperatorWithdraw, waText},
		{true, distrtypes.NewMsgWithdrawDelegatorReward(e.bech(opB), valoper), sstypes.ErrOperatorRewardClaim, claimText},
		{true, distrtypes.NewMsgWithdrawValidatorCommission(valoper), sstypes.ErrOperatorRewardClaim, claimText},
	} {
		key := e.user
		if c.signer {
			key = vBKey
		}
		res := e.checkTx(e.signedTx(key, 400_000, 5_000, c.m))
		require.Equal(t, c.err.(interface{ ABCICode() uint32 }).ABCICode(), res.Code, "%T: %s", c.m, res.Log)
		require.Contains(t, res.Log, c.text, "%T", c.m)
	}

	// Past the ante, the in-module refusals carry the same text: the staking
	// hook (x/staking itself) and the app's router.
	cc, _ := e.ctx().CacheContext()
	m := stakingtypes.NewMsgDelegate(user, valoper, amt)
	_, err := e.app.MsgServiceRouter().Handler(m)(cc, m)
	require.ErrorIs(t, err, staking)
	require.Contains(t, err.Error(), stakingText)
	cc, _ = e.ctx().CacheContext()
	mc := distrtypes.NewMsgWithdrawValidatorCommission(valoper)
	_, err = e.app.rewardsRouter.Handler(mc)(cc, mc)
	require.Contains(t, err.Error(), claimText)

	// The operator's exit: undelegating its own self-bond still works.
	fb := e.run(e.signedTx(vBKey, 400_000, 5_000, stakingtypes.NewMsgUndelegate(e.bech(opB), valoper, amt)))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
}
