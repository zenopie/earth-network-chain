package app

// Regression tests for the 2026-10 x/shieldedstaking audit. Each started as
// the auditor's proof of concept (which passed against bae86fa) and now
// asserts the fail-safe outcome.

import (
	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	"encoding/json"
	"fmt"
	"sort"
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

// F1: with more books than one block processes, the books past the cap
// starved for ever (the epoch end walked from the first key each time). The
// sweep now goes on from a cursor in the following blocks: every book is
// processed within ceil(books / EpochValidatorLimit) blocks of the epoch end.
func TestAuditEpochValidatorStarvation(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)

	var vals []sdk.ValAddress
	for i := 0; i < sstypes.EpochValidatorLimit+5; i++ {
		v, _ := e.createValidator(1) // 1uerth self-bond: unbonded, not jailed
		vals = append(vals, v)
	}
	e.next(5 * time.Second)
	for i, v := range vals {
		e.auditDelegate(v, uint64(ssErth), fmt.Sprintf("book-%d", i)) // the minimum: 1 ERTH
	}
	sort.Slice(vals, func(i, j int) bool { return e.valoper(vals[i]) < e.valoper(vals[j]) })
	victim := vals[len(vals)-1] // sorts last
	e.auditDelegate(victim, uint64(1_000*ssErth), "victim")

	e.next(25 * time.Hour) // epoch end: the first EpochValidatorLimit books
	sweep, err := e.app.ShieldedStakingKeeper.EpochSweep.Get(e.ctx())
	require.NoError(t, err)
	require.True(t, sweep.Active, "the sweep goes on next block")
	require.True(t, e.modDelegation(vals[0]).IsPositive())
	require.True(t, e.modDelegation(victim).IsZero(), "not yet: past the cap")

	e.next(5 * time.Second) // the rest
	sweep, err = e.app.ShieldedStakingKeeper.EpochSweep.Get(e.ctx())
	require.NoError(t, err)
	require.False(t, sweep.Active, "the sweep reached the last book")
	for _, v := range vals {
		require.True(t, e.modDelegation(v).IsPositive(), e.valoper(v))
	}
	require.True(t, e.modDelegation(victim).GTE(math.NewInt(1_000*ssErth)))
	require.True(t, e.state(victim).PendingDelegation.LT(math.NewInt(ssErth)), "only rewards stay queued")
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(e.ctx()))
}

// F1: books can no longer be created for dust: a delegation below
// min_delegation (1 ERTH), or one that would mint less than min_delegation
// derth, is refused.
func TestAuditMinDelegation(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1)
	e.next(5 * time.Second)
	m := auditDelegateMsg(e.valoper(v), uint64(ssErth)-1, "dust")
	_, err := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper).Delegate(e.fakeAuthorized(m), m)
	require.ErrorContains(t, err, "at least")
	_, err = sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).CheckPrivateAction(e.ctx(), m)
	require.ErrorContains(t, err, "at least")
	has, err := e.app.ShieldedStakingKeeper.Validators.Has(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.False(t, has)
}

// F2: the snapshot a deposit takes when it activates voting walked every
// book (a reward computation each) inside the deposit tx: enough books made
// every such deposit run out of gas. It is O(1) now: the same gas with 1
// book or 60.
func TestAuditSnapshotGasIndependentOfBooks(t *testing.T) {
	gasWith := func(books int) uint64 {
		e := initStakeEnv(t)
		e.auditFundPool(10_000 * ssErth)
		for i := 0; i < books; i++ {
			v, _ := e.createValidator(1)
			e.next(5 * time.Second)
			e.auditDelegate(v, uint64(ssErth), fmt.Sprintf("g-%d", i))
		}
		e.next(25 * time.Hour)
		e.next(5 * time.Second)
		prop := e.submitProposal()
		k := e.app.ShieldedStakingKeeper
		ctx := e.ctx()
		snap, err := k.Snapshots.Get(ctx, prop)
		require.NoError(t, err)
		require.NoError(t, k.Snapshots.Remove(ctx, prop))
		require.NoError(t, k.SnapshotsBySeq.Remove(ctx, collections.Join(snap.Seq, prop)))
		gctx := ctx.WithGasMeter(storetypes.NewGasMeter(1 << 40))
		require.NoError(t, k.GovHooks().AfterProposalDeposit(gctx, prop, nil))
		require.True(t, must(k.Snapshots.Has(gctx, prop)))
		return gctx.GasMeter().GasConsumed()
	}
	one, sixty := gasWith(1), gasWith(60)
	t.Logf("snapshot gas: 1 book %d, 60 books %d", one, sixty)
	require.Equal(t, one, sixty)
	require.Less(t, sixty, uint64(100_000))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
