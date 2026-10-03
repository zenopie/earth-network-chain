package app

// Regression tests for the 2026-10 x/shieldedstaking audit. Each started as
// the auditor's proof of concept (which passed against bae86fa) and now
// asserts the fail-safe outcome.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// auditFundPool mints uerth into the shielded pool (as if shielded) so
// fake-authorized Delegates can release it.
func (e *stakeEnv) auditFundPool(amt int64) {
	ctx := e.ctx()
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", amt))
	require.NoError(e.t, e.app.BankKeeper.MintCoins(ctx, shieldedtypes.ModuleName, coins))
	t, err := e.app.ShieldedKeeper.Turnstile(ctx, "uerth")
	require.NoError(e.t, err)
	t.In = t.In.Add(math.NewInt(amt))
	require.NoError(e.t, e.app.ShieldedKeeper.Turnstiles.Set(ctx, "uerth", t))
}

func auditDelegateMsg(valoper string, amt uint64, label string) *sstypes.MsgDelegate {
	return &sstypes.MsgDelegate{
		Bundle:    stubBundle(label, shieldedtypes.ValueBalance{Denom: "uerth", Amount: amt + 1}),
		Fee:       1,
		Validator: valoper,
		Stake:     sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("audit-pc/"+label, 0))},
	}
}

// auditDelegate drives MsgDelegate's handler (ante faked) for amt uerth.
func (e *stakeEnv) auditDelegate(val sdk.ValAddress, amt uint64, label string) *sstypes.MsgDelegateResponse {
	m := auditDelegateMsg(e.valoper(val), amt, label)
	res, err := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper).Delegate(e.fakeAuthorized(m), m)
	require.NoError(e.t, err)
	return res
}

// F0: an all-uppercase bech32 alias of a validator's operator address was a
// second book over the same SDK delegation; 1uerth of alias derth undelegated
// everyone's stake. Every entry point must now refuse a non-canonical string.
func TestAuditValoperCaseAliasDrainsDelegation(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(v, uint64(1_000*ssErth), "victim")
	e.next(25 * time.Hour)
	canon := e.valoper(v)
	before := e.modDelegation(v)
	require.True(t, before.IsPositive())

	for _, alias := range []string{strings.ToUpper(canon), canon[:5] + strings.ToUpper(canon[5:8]) + canon[8:]} {
		m := auditDelegateMsg(alias, 1, "atk")
		require.Error(t, m.ValidateBasic(), alias)
		srv := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper)
		_, err := srv.Delegate(e.fakeAuthorized(m), m)
		require.Error(t, err)
		_, err = sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).CheckPrivateAction(e.ctx(), m)
		require.Error(t, err)

		u := &sstypes.MsgUndelegate{Validator: alias, Amount: 1,
			Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("atk-pc", 1))}}
		require.Error(t, u.ValidateBasic())
		_, err = srv.Undelegate(e.fakeAuthorized(u), u)
		require.Error(t, err)

		_, _, err = e.app.ShieldedStakingKeeper.Backing(e.ctx(), alias)
		require.Error(t, err)

		for _, msg := range []interface{ ValidateBasic() error }{
			&sstypes.MsgRestake{Validator: alias}, &sstypes.MsgClaimUnbonding{Validator: alias, Amount: 1},
			&sstypes.MsgStakeVote{Validator: alias, Weight: 1}, &sstypes.MsgLockPosition{Validator: alias, Amount: 1},
		} {
			require.Error(t, msg.ValidateBasic(), "%T", msg)
		}
		_, err = sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Validator(e.ctx(), &sstypes.QueryValidatorRequest{Validator: alias})
		require.Error(t, err)
	}
	has, err := e.app.ShieldedStakingKeeper.Validators.Has(e.ctx(), strings.ToUpper(canon))
	require.NoError(t, err)
	require.False(t, has, "no alias book was created")
	e.next(25 * time.Hour)
	require.True(t, e.modDelegation(v).GTE(before), "the honest delegation is intact")
}

// F0 at genesis: a book keyed by a non-canonical validator string is refused.
func TestAuditGenesisRefusesNonCanonicalValoper(t *testing.T) {
	var err error
	defer func() {
		r := recover()
		require.NotNil(t, r, "InitChain refuses it")
		require.Contains(t, fmt.Sprint(r), "not canonical")
	}()
	_, err = initStakeEnvWith(t, func(appState map[string]json.RawMessage, op sdk.AccAddress) {
		var gs map[string]any
		require.NoError(t, json.Unmarshal(appState[sstypes.ModuleName], &gs))
		gs["validators"] = []any{map[string]any{
			"validator": strings.ToUpper(sdk.ValAddress(op).String()), "pending_delegation": "0",
			"pending_undelegation": "0", "epoch_rate": "1.000000000000000000", "derth_supply": "0",
		}}
		bz, err := json.Marshal(gs)
		require.NoError(t, err)
		appState[sstypes.ModuleName] = bz
	})
	require.ErrorContains(t, err, "not canonical")
	panic(err)
}
