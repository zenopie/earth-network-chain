package app

// Regression tests for the 2026-10 x/shieldedstaking audit. Each started as
// the auditor's proof of concept (which passed against bae86fa) and now
// asserts the fail-safe outcome.

import (
	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"encoding/json"
	"fmt"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"sort"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	earthtypes "github.com/earth-network/earth/x/earth/types"
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
	// The pool's account cannot mint (and refuses plain sends): shield.
	require.NoError(e.t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(e.t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, e.userAddr(), coins))
	_, _, err := e.app.ShieldedKeeper.Shield(ctx, e.userAddr(), coins[0], privacy.FieldBytes(ssDet("audit-fund", uint64(amt))), nil)
	require.NoError(e.t, err)
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

// F3: zero-height export withdrew the module's rewards unbooked and zeroed
// the SDK unbonding entries' creation heights under the module's records,
// so the exported genesis failed x/shieldedstaking's InitGenesis invariants.
// Now the rewards are booked into the queues first and the records follow
// the reset: the export re-imports, and the unbonding still pays.
func TestAuditZeroHeightExportBreaksInvariants(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	res := e.auditDelegate(v, uint64(1_000*ssErth), "z")
	e.next(25 * time.Hour)
	// an unbonding in flight across the export
	u := &sstypes.MsgUndelegate{Validator: e.valoper(v), Amount: res.Derth / 4,
		Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("audit-pc/z", 1))}}
	_, err := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper).Undelegate(e.fakeAuthorized(u), u)
	require.NoError(t, err)
	e.next(25 * time.Hour)
	for i := 0; i < 20; i++ {
		e.next(5 * time.Second)
	}
	require.True(t, e.pendingRewards(v).IsPositive())
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(e.ctx()))

	// prep alone keeps the books whole
	ctx, _ := e.ctx().CacheContext()
	e.app.prepForZeroHeightGenesis(ctx, nil)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))

	// and the export re-imports
	exported, err := e.app.ExportAppStateAndValidators(true, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: 1, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err, "InitGenesis runs AssertInvariants")
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))
	var rec sstypes.UnbondRecord
	require.NoError(t, fresh.ShieldedStakingKeeper.UnbondRecords.Walk(fctx, nil,
		func(_ collections.Pair[string, uint64], r sstypes.UnbondRecord) (bool, error) {
			rec = r
			return true, nil
		}))
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, rec.Status)
	require.Zero(t, rec.CreationHeight)
	gs, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	q := math.ZeroInt()
	for _, b := range gs.Validators {
		q = q.Add(b.PendingDelegation)
	}
	require.True(t, q.IsPositive(), "the withdrawn rewards are queued")
}

// F5: a book whose last derth left kept the rewards its delegation earned
// after the last notes were minted (and any donation), with no derth against
// them: the next delegator minted at rate 1 and took them. Now a delegation
// to such a book is refused until the epoch end settles it: the last records
// take the whole delegation, and what is left queued goes to the community
// pool; the book empties and is removed.
func TestAuditOrphanBackingNotCaptured(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	res := e.auditDelegate(v, uint64(100*ssErth), "o")
	e.next(25 * time.Hour)
	srv := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper)
	u := &sstypes.MsgUndelegate{Validator: e.valoper(v), Amount: res.Derth,
		Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("audit-pc/o", 1))}}
	ures, err := srv.Undelegate(e.fakeAuthorized(u), u)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		e.next(5 * time.Second)
	}
	b, s, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.True(t, s.IsZero())
	require.True(t, b.IsPositive(), "rewards accrued after the last notes were minted")
	m := auditDelegateMsg(e.valoper(v), uint64(ssErth), "late")
	_, err = srv.Delegate(e.fakeAuthorized(m), m)
	require.ErrorContains(t, err, "settling")

	e.next(25 * time.Hour) // the epoch end settles the book
	require.True(t, e.modDelegation(v).IsZero(), "the whole delegation went out with the last record")
	has, err := e.app.ShieldedStakingKeeper.Validators.Has(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.False(t, has, "the book is gone")
	var rec sstypes.UnbondRecord
	require.NoError(t, e.app.ShieldedStakingKeeper.UnbondRecords.Walk(e.ctx(), nil,
		func(_ collections.Pair[string, uint64], r sstypes.UnbondRecord) (bool, error) {
			rec = r
			return true, nil
		}))
	require.Equal(t, math.NewIntFromUint64(ures.Value), rec.Requested)
	require.True(t, rec.Undelegated.GT(rec.Requested), "the last holders take what their stake earned")
	e.invariants()

	// a new delegator starts at rate 1 with nothing to capture
	e.auditDelegate(v, uint64(ssErth), "fresh")
	b, s, err = e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.Equal(t, b, s)
}

// F7: an operator that removed its whole self-bond had its reward escrow
// frozen for as long as any private derth stayed delegated to its validator
// (x/staking keeps the validator until no delegation is left). Now the
// escrow is paid out once the unbonding time has passed.
func TestAuditEscrowReleasedOnRetirement(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vB, uint64(10*ssErth), "dust-forever")
	e.next(25 * time.Hour)
	opB, escB, valoper := sdk.AccAddress(vB), sstypes.RewardEscrowAddress(vB), e.valoper(vB)
	bal := func(a sdk.AccAddress) math.Int { return e.app.BankKeeper.GetBalance(e.ctx(), a, "uerth").Amount }
	d, err := e.app.StakingKeeper.GetDelegation(e.ctx(), opB, vB)
	require.NoError(t, err)
	val, err := e.app.StakingKeeper.GetValidator(e.ctx(), vB)
	require.NoError(t, err)
	e.next(time.Hour)
	fb := e.run(e.signedTx(vBKey, 400_000, 5_000, stakingtypes.NewMsgUndelegate(e.bech(opB), valoper,
		sdk.NewCoin("uerth", val.TokensFromShares(d.Shares).TruncateInt()))))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	held := bal(escB)
	require.True(t, held.IsPositive())
	op0 := bal(opB)
	var released bool
	for i := 0; i < 25 && !released; i++ {
		r := e.next(24 * time.Hour)
		for _, ev := range eventsOf(r.Events, sstypes.EventTypeEscrowReleased) {
			released = released || ev["validator"] == valoper
		}
	}
	require.True(t, released, "escrow released after retirement")
	_, err = e.app.StakingKeeper.GetValidator(e.ctx(), vB)
	require.NoError(t, err, "the validator lives on: private derth is still delegated to it")
	require.True(t, bal(escB).IsZero())
	require.True(t, bal(opB).Sub(op0).GTE(held), "paid to the operator")
	has, err := e.app.ShieldedStakingKeeper.RewardEscrows.Has(e.ctx(), escB)
	require.NoError(t, err)
	require.True(t, has, "still recorded: the validator exists")
	e.invariants()
}

// F9: UpdatePosition accepted a split on a position with no weight
// (LockPosition refused it).
func TestAuditUpdatePositionNeedsWeight(t *testing.T) {
	e := initStakeEnv(t)
	k := e.app.ShieldedStakingKeeper
	gov := authtypes.NewModuleAddress("gov")
	_, err := allocationkeeper.NewMsgServerImpl(e.app.AllocationKeeper).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: allocationtypes.STREAM_ID_GROUNDWORKS, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	ctx := e.ctx()
	v := e.valoper(e.genesisValidator())
	vs, err := k.ValidatorState(ctx, v)
	require.NoError(t, err)
	vs.EpochRate = math.LegacyNewDecWithPrec(1, 18) // weight floors to zero
	require.NoError(t, k.Validators.Set(ctx, v, vs))
	tag := privacy.FieldBytes(ssDet("audit-owner", 0))
	require.NoError(t, k.Positions.Set(ctx, 7, sstypes.Position{Id: 7, Validator: v, Derth: math.NewInt(ssErth),
		OwnerTag: tag, Weight: math.ZeroInt()}))
	m := &sstypes.MsgUpdatePosition{PositionId: 7, Splits: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}},
		Stake: sstypes.StakeProof{OwnerTag: tag}}
	_, err = sskeeper.NewActionHandler(k).CheckPrivateAction(ctx, m)
	require.ErrorIs(t, err, allocationtypes.ErrNoWeight)
}

// F5, legacy books: a delegation with no derth and no record against it (as
// the old epoch end could leave) is undelegated into an orphan record, whose
// payout goes to the community pool when it matures; the book is removed.
func TestAuditOrphanDelegationToCommunityPool(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(v, uint64(5*ssErth), "legacy")
	e.next(25 * time.Hour)
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	vs, err := k.ValidatorState(ctx, e.valoper(v))
	require.NoError(t, err)
	vs.DerthSupply = math.ZeroInt() // nobody holds derth/v any more
	require.NoError(t, k.Validators.Set(ctx, e.valoper(v), vs))
	orphan := e.modDelegation(v)
	require.True(t, orphan.IsPositive())

	e.next(25 * time.Hour)
	require.True(t, e.modDelegation(v).IsZero())
	pool0, err := e.app.DistrKeeper.FeePool.Get(e.ctx())
	require.NoError(t, err)
	e.invariants()
	for i := 0; i < 23; i++ {
		e.next(24 * time.Hour)
	}
	e.invariants()
	n := 0
	require.NoError(t, k.UnbondRecords.Walk(e.ctx(), nil, func(collections.Pair[string, uint64], sstypes.UnbondRecord) (bool, error) {
		n++
		return false, nil
	}))
	require.Zero(t, n, "the orphan record was swept")
	has, err := k.Validators.Has(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.False(t, has)
	pool1, err := e.app.DistrKeeper.FeePool.Get(e.ctx())
	require.NoError(t, err)
	require.True(t, pool1.CommunityPool.AmountOf("uerth").Sub(pool0.CommunityPool.AmountOf("uerth")).GTE(math.LegacyNewDecFromInt(orphan)))
}

// F4: a donation to a validator's rewards pool (MsgDepositValidatorRewardsPool)
// raises the rate of its derth. Against a book reduced to 1 derth it made
// the next delegation round to almost nothing, the loss going to the 1
// derth's holder. A delegation must now mint at least min_delegation derth,
// so its rounding loss is at most a millionth of it; one too small for that
// is refused, not rounded away.
func TestAuditDonationInflationHarmless(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	res := e.auditDelegate(v, uint64(ssErth), "atk")
	e.next(25 * time.Hour)
	srv := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper)
	u := &sstypes.MsgUndelegate{Validator: e.valoper(v), Amount: res.Derth - 1,
		Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("audit-pc/atk", 1))}}
	_, err := srv.Undelegate(e.fakeAuthorized(u), u)
	require.NoError(t, err)
	e.next(25 * time.Hour)
	require.Equal(t, math.OneInt(), e.app.ShieldedStakingKeeper.Supply(e.ctx(), e.valoper(v)))
	_, err = distrkeeper.NewMsgServerImpl(e.app.DistrKeeper).DepositValidatorRewardsPool(e.ctx(), &distrtypes.MsgDepositValidatorRewardsPool{
		Depositor: e.bech(e.userAddr()), ValidatorAddress: e.valoper(v), Amount: sdk.NewCoins(sdk.NewInt64Coin("uerth", 1_000*ssErth)),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	b, s, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	t.Logf("after the donation: backing %s for %s derth", b, s)
	require.True(t, b.GT(math.NewInt(1000)))

	m := auditDelegateMsg(e.valoper(v), uint64(100*ssErth), "victim")
	_, err = srv.Delegate(e.fakeAuthorized(m), m)
	require.ErrorContains(t, err, "less than the minimum")
	// a delegation that mints enough loses under a millionth to rounding
	big := b.MulRaw(ssErth).Add(b).Uint64()
	e.auditFundPool(int64(big) + ssErth)
	vr := e.auditDelegate(v, big, "big")
	b2, s2, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	value := math.NewIntFromUint64(vr.Derth).Mul(b2).Quo(s2)
	loss := math.NewIntFromUint64(big).Sub(value)
	require.True(t, loss.MulRaw(ssErth).LTE(math.NewIntFromUint64(big)), "loss %s of %d", loss, big)
}
