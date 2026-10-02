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
	fb := e.run(e.signedTx(e.user, 300_000, 5_000, setWA(user, opB)))
	require.Equal(t, distrtypes.ErrSetWithdrawAddrDisabled.ABCICode(), fb.Code, fb.Log)
	for _, m := range []sdk.Msg{setWA(user, opB), setWA(opB, user)} {
		cc, _ := e.ctx().CacheContext()
		_, err := e.app.MsgServiceRouter().Handler(m)(cc, m)
		require.ErrorIs(t, err, distrtypes.ErrSetWithdrawAddrDisabled)
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
