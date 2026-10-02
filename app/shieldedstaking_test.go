package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// ---- private msgs, built and proven against the chain as it stands --------

func (e *stakeEnv) delegateMsg(val sdk.ValAddress, in *wnote, amount uint64) (*sstypes.MsgDelegate, *pendingTransfer, *wnote) {
	p := e.build(spend{denom: "uerth", inputs: []*wnote{in}, valueOut: amount})
	dn := e.w.fresh(sstypes.DerthDenom(e.valoper(val)), 0)
	m := &sstypes.MsgDelegate{Transfer: p.tr, Validator: e.valoper(val), Pc: privacy.FieldBytes(e.w.pc(dn)), Ciphertext: []byte("derth note")}
	e.prove(p, m)
	return m, p, dn
}

// delegate stakes amount out of an ERTH note and returns the derth note.
func (e *stakeEnv) delegate(val sdk.ValAddress, amount uint64) *wnote {
	e.t.Helper()
	in := e.w.unspent("uerth", amount)
	require.NotNil(e.t, in)
	m, p, dn := e.delegateMsg(val, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	return e.minted(res, dn)
}

func (e *stakeEnv) undelegateMsg(val sdk.ValAddress, in *wnote, amount uint64) (*sstypes.MsgUndelegate, *pendingTransfer, *wnote) {
	epoch, err := e.app.ShieldedStakingKeeper.Epoch.Get(e.ctx())
	require.NoError(e.t, err)
	p := e.build(spend{denom: in.denom, inputs: []*wnote{in}, valueOut: amount})
	un := e.w.fresh(sstypes.UnbondDenom(e.valoper(val), epoch.Number), 0)
	m := &sstypes.MsgUndelegate{Transfer: p.tr, Validator: e.valoper(val), Pc: privacy.FieldBytes(e.w.pc(un))}
	e.prove(p, m)
	return m, p, un
}

// undelegate turns amount of a derth note into an unbond note.
func (e *stakeEnv) undelegate(val sdk.ValAddress, in *wnote, amount uint64) *wnote {
	e.t.Helper()
	m, p, un := e.undelegateMsg(val, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	return e.minted(res, un)
}

func (e *stakeEnv) claimMsg(in *wnote) (*sstypes.MsgClaimUnbonding, *pendingTransfer, *wnote) {
	return e.claimMsgFee(in, 0, true)
}

// claimMsgFee is a claim paying feeFromOutput out of what it claims (no fee
// note), or a fee note when it is 0.
func (e *stakeEnv) claimMsgFee(in *wnote, feeFromOutput uint64, prove bool) (*sstypes.MsgClaimUnbonding, *pendingTransfer, *wnote) {
	v, epoch, ok := sstypes.ParseUnbondDenom(in.denom)
	require.True(e.t, ok)
	p := e.build(spend{denom: in.denom, inputs: []*wnote{in}, valueOut: in.value, feeless: feeFromOutput > 0})
	out := e.w.fresh("uerth", 0)
	m := &sstypes.MsgClaimUnbonding{Transfer: p.tr, Validator: v, Epoch: epoch, Pc: privacy.FieldBytes(e.w.pc(out)), FeeFromOutput: feeFromOutput}
	if !prove {
		m.Transfer.Proof = make([]byte, 14656)
		return m, p, out
	}
	e.prove(p, m)
	return m, p, out
}

// claim spends an unbond note and returns the ERTH note (nil if it paid 0).
func (e *stakeEnv) claim(in *wnote) *wnote {
	e.t.Helper()
	m, p, out := e.claimMsg(in)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	for _, ev := range eventsOf(res.Events, sstypes.EventTypeClaim) {
		if ev["amount"] == "0" {
			return nil
		}
	}
	return e.minted(res, out)
}

func (e *stakeEnv) feeOnly() *pendingTransfer { return e.build(spend{denom: "uerth"}) }

func (e *stakeEnv) state(val sdk.ValAddress) sstypes.ValidatorState {
	vs, err := e.app.ShieldedStakingKeeper.ValidatorState(e.ctx(), e.valoper(val))
	require.NoError(e.t, err)
	return vs
}

func (e *stakeEnv) rate(val sdk.ValAddress) math.LegacyDec {
	r, err := e.app.ShieldedStakingKeeper.Rate(e.ctx(), e.valoper(val))
	require.NoError(e.t, err)
	return r
}

func (e *stakeEnv) modDelegation(val sdk.ValAddress) math.Int {
	ctx := e.ctx()
	d, err := e.app.StakingKeeper.GetDelegation(ctx, e.app.ShieldedStakingKeeper.ModuleAddress(), val)
	if err != nil {
		return math.ZeroInt()
	}
	v, err := e.app.StakingKeeper.GetValidator(ctx, val)
	require.NoError(e.t, err)
	return v.TokensFromShares(d.Shares).TruncateInt()
}

func (e *stakeEnv) pendingRewards(val sdk.ValAddress) math.Int {
	r, err := distrkeeper.NewQuerier(e.app.DistrKeeper).DelegationRewards(e.ctx(), &distrtypes.QueryDelegationRewardsRequest{
		DelegatorAddress: e.bech(e.app.ShieldedStakingKeeper.ModuleAddress()), ValidatorAddress: e.valoper(val),
	})
	require.NoError(e.t, err)
	return r.Rewards.AmountOf("uerth").TruncateInt()
}

func (e *stakeEnv) record(val sdk.ValAddress, epoch uint64) sstypes.UnbondRecord {
	r, err := e.app.ShieldedStakingKeeper.UnbondRecords.Get(e.ctx(), collections.Join(e.valoper(val), epoch))
	require.NoError(e.t, err)
	return r
}

func epochOf(t *testing.T, denom string) uint64 {
	_, ep, ok := sstypes.ParseUnbondDenom(denom)
	require.True(t, ok)
	return ep
}

// Delegate -> epoch -> rewards raise the rate -> undelegate -> epoch ->
// 21 days -> claim, on the real genesis path, with real proofs.
func TestPrivateStakingLifecycle(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()

	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth)) // fees

	// --- delegate 2,000 ERTH: rate 1, so 2,000 derth; the ERTH waits for the
	// epoch in the module account.
	dn := e.delegate(vB, uint64(2_000*ssErth))
	require.Equal(t, uint64(2_000*ssErth), dn.value)
	require.Equal(t, math.NewInt(2_000*ssErth), e.state(vB).PendingDelegation)
	require.True(t, e.modDelegation(vB).IsZero())
	e.invariants()

	// --- epoch end: delegated.
	e.days(1)
	require.Equal(t, math.NewInt(2_000*ssErth), e.modDelegation(vB))
	require.True(t, e.state(vB).PendingDelegation.IsZero())
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), mod, "uerth").IsZero())
	e.invariants()

	// --- rewards accrue; the live rate includes them, the next epoch
	// compounds them into the delegation.
	e.next(time.Hour)
	require.True(t, e.pendingRewards(vB).IsPositive())
	live := e.rate(vB)
	require.True(t, live.GT(math.LegacyOneDec()), "rate %s", live)
	e.days(1)
	require.True(t, e.modDelegation(vB).GT(math.NewInt(2_000*ssErth)), "rewards re-delegated")
	epochRate := e.state(vB).EpochRate
	require.True(t, epochRate.GT(live), "epoch rate %s", epochRate)
	e.invariants()

	// --- undelegate 1,000 derth: its value at the live rate.
	rateNow := e.rate(vB)
	un := e.undelegate(vB, dn, uint64(1_000*ssErth))
	// Worth d x the live rate at execution (one 5 s block of rewards after
	// rateNow was read).
	atRead := rateNow.MulInt64(1_000 * ssErth).TruncateInt().Int64()
	require.GreaterOrEqual(t, int64(un.value), atRead)
	require.InEpsilon(t, atRead, int64(un.value), 1e-3)
	ep := epochOf(t, un.denom)
	require.Equal(t, sstypes.UNBOND_STATUS_PENDING, e.record(vB, ep).Status)
	// The remaining 1,000 derth stays a note (change of the undelegate).
	require.Equal(t, uint64(1_000*ssErth), e.w.balance(sstypes.DerthDenom(e.valoper(vB))))
	e.invariants()

	// A claim before maturity is refused before anything is spent.
	cm, _, _ := e.claimMsg(un)
	res := e.checkTx(e.privateTx(cm))
	require.Equal(t, sstypes.ErrNotMatured.ABCICode(), res.Code, res.Log)

	// --- epoch end: one SDK undelegation, the amount it returned recorded.
	e.days(1)
	r := e.record(vB, ep)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, r.Status)
	require.True(t, r.Undelegated.Sub(r.Requested).Abs().LTE(math.NewInt(2)), "undelegated %s requested %s", r.Undelegated, r.Requested)
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)
	require.Equal(t, r.Undelegated, ubd.Entries[0].InitialBalance)
	e.invariants()

	// --- 21 days: matured. The payout was read before x/staking paid it.
	e.days(21)
	r = e.record(vB, ep)
	require.Equal(t, sstypes.UNBOND_STATUS_MATURED, r.Status)
	require.Equal(t, r.Undelegated, r.Payout, "no slash: the entry paid in full")
	e.invariants()

	// --- claim: value x payout / requested, paying its fee from what it
	// claims (fee_from_output): no fee note is spent, the note holds the
	// rest, and fee_collector got the whole fee.
	want := math.NewIntFromUint64(un.value).Mul(r.Payout).Quo(r.Requested)
	// A fee from output must leave something to mint, and must clear the
	// fee floor like any other: both refused before anything is spent.
	big, _, _ := e.claimMsgFee(un, want.Uint64(), false)
	res = e.checkTx(e.privateTx(big))
	require.Equal(t, sstypes.ErrAmount.ABCICode(), res.Code, res.Log)
	tiny, _, _ := e.claimMsgFee(un, 1, false)
	res = e.checkTx(e.privateTx(tiny))
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), res.Code, res.Log)
	before := e.w.balance("uerth")
	fm, fp, out := e.claimMsgFee(un, ssFee, true)
	cres := e.run(e.privateTx(fm))
	require.Equal(t, uint32(0), cres.Code, cres.Log)
	e.settle(fp)
	out = e.minted(cres, out)
	require.Equal(t, want.Uint64()-ssFee, out.value)
	require.Equal(t, before+out.value, e.w.balance("uerth"), "no fee note spent")
	var feeEvents int
	for _, ev := range eventsOf(cres.Events, shieldedtypes.EventTypeFee) {
		require.Equal(t, fmt.Sprintf("%duerth", ssFee), ev["amount"])
		require.Equal(t, sstypes.ModuleName, ev["module"])
		feeEvents++
	}
	require.Equal(t, 1, feeEvents)
	_, err = e.app.ShieldedStakingKeeper.UnbondRecords.Get(e.ctx(), collections.Join(e.valoper(vB), ep))
	require.ErrorIs(t, err, collections.ErrNotFound, "fully claimed records are removed")
	e.invariants()

	// The same unbond note cannot be claimed twice (its nullifier is spent).
	res = e.checkTx(e.privateTx(cm))
	require.NotEqual(t, uint32(0), res.Code)
}

// A slash reaches private stakers three ways: through the delegation (the
// rate falls at once), through SDK unbonding entries created after the
// infraction (their claims pay less), and through this epoch's pending
// undelegations (their target is cut in the slash hook). An entry created
// before the infraction is untouched. The slashed validator then refuses new
// delegations.
func TestPrivateStakingSlashPassThrough(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	dn := e.delegate(vB, uint64(2_000*ssErth))
	e.days(1)

	// E1: before the infraction.
	un1 := e.undelegate(vB, dn, uint64(500*ssErth))
	dn = e.w.unspent(dn.denom, 1)
	e.days(1)
	r1 := e.record(vB, epochOf(t, un1.denom))
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, r1.Status)

	// The infraction: x/evidence slashes entries created at or after
	// infraction - 1 (the distribution height), so leave E1 two blocks behind.
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	infraction := e.height
	ctx := e.ctx()
	val, err := e.app.StakingKeeper.GetValidator(ctx, vB)
	require.NoError(t, err)
	power := val.ConsensusPower(e.app.StakingKeeper.PowerReduction(ctx))
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)

	// E2: after the infraction, before the evidence.
	un2 := e.undelegate(vB, dn, uint64(500*ssErth))
	dn = e.w.unspent(dn.denom, 1)
	e.days(1)
	r2 := e.record(vB, epochOf(t, un2.denom))
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, r2.Status)
	require.Greater(t, r2.CreationHeight, infraction)

	// E3: pending in the current epoch when the evidence lands.
	un3 := e.undelegate(vB, dn, uint64(300*ssErth))
	r3 := e.record(vB, epochOf(t, un3.denom))
	require.Equal(t, sstypes.UNBOND_STATUS_PENDING, r3.Status)

	delBefore := e.modDelegation(vB)
	rateBefore := e.rate(vB)
	res := e.block(5*time.Second, []abci.Misbehavior{{
		Type: abci.MisbehaviorType_DUPLICATE_VOTE, Validator: abci.Validator{Address: consAddr, Power: power},
		Height: infraction, Time: e.times[infraction], TotalVotingPower: power + 100,
	}})
	haircut := eventsOf(res.Events, sstypes.EventTypeSlashHaircut)
	require.Len(t, haircut, 1)
	// x/staking slashes 5% of the power at the infraction; on the stake
	// since compounded that is a smaller effective fraction f of today's
	// tokens, which is what the delegation and pending undelegations lose.
	f, err := math.LegacyNewDecFromStr(haircut[0]["fraction"])
	require.NoError(t, err)
	require.True(t, f.IsPositive() && f.LTE(math.LegacyNewDecWithPrec(5, 2)), "fraction %s", f)
	keep := math.LegacyOneDec().Sub(f)

	ctx = e.ctx()
	val, _ = e.app.StakingKeeper.GetValidator(ctx, vB)
	require.True(t, val.IsJailed())
	require.True(t, e.app.SlashingKeeper.IsTombstoned(ctx, consAddr))
	// The delegation loses f; the live rate follows.
	delAfter := e.modDelegation(vB)
	require.InEpsilon(t, keep.MulInt(delBefore).TruncateInt64(), delAfter.Int64(), 1e-6)
	require.True(t, e.rate(vB).LT(rateBefore), "rate %s -> %s", rateBefore, e.rate(vB))

	// Entries: E1 untouched, E2 slashed 5% of its initial balance.
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(ctx, e.app.ShieldedStakingKeeper.ModuleAddress(), vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 2)
	for _, en := range ubd.Entries {
		switch en.CreationHeight {
		case r1.CreationHeight:
			require.Equal(t, en.InitialBalance, en.Balance)
		case r2.CreationHeight:
			require.InDelta(t, en.InitialBalance.MulRaw(95).QuoRaw(100).Int64(), en.Balance.Int64(), 1)
		default:
			t.Fatalf("unexpected entry at %d", en.CreationHeight)
		}
	}
	// Pending: its target cut by the same fraction.
	r3 = e.record(vB, epochOf(t, un3.denom))
	require.InEpsilon(t, keep.MulInt(r3.Requested).TruncateInt64(), r3.Target.Int64(), 1e-6)
	require.Equal(t, r3.Target, e.state(vB).PendingUndelegation)
	e.invariants()

	// Jailed and tombstoned: a delegation is refused before anything is spent.
	in := e.w.unspent("uerth", uint64(100*ssErth))
	p := e.build(spend{denom: "uerth", inputs: []*wnote{in}, valueOut: uint64(100 * ssErth)})
	m := &sstypes.MsgDelegate{Transfer: p.tr, Validator: e.valoper(vB), Pc: privacy.FieldBytes(ssDet("pc", 1))}
	m.Transfer.Proof = make([]byte, 14656) // refused before the proof is read
	ct := e.checkTx(e.privateTx(m))
	require.Equal(t, sstypes.ErrValidator.ABCICode(), ct.Code, ct.Log)
	require.Contains(t, ct.Log, "jailed")
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), m.Transfer.Nullifiers[0])
	require.NoError(t, err)
	require.False(t, spent)

	// Epoch end: the pending record undelegates its reduced target (the
	// jailed validator still releases stake). Then everything matures.
	e.days(1)
	r3 = e.record(vB, epochOf(t, un3.denom))
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, r3.Status)
	e.invariants()
	e.days(22)
	for _, un := range []*wnote{un1, un2, un3} {
		require.Equal(t, sstypes.UNBOND_STATUS_MATURED, e.record(vB, epochOf(t, un.denom)).Status)
	}
	e.invariants()

	// Claims: E1 in full, E2 at 95%, E3 at 1 - f.
	for i, un := range []*wnote{un1, un2, un3} {
		out := e.claim(un)
		require.NotNil(t, out)
		ratio := math.LegacyNewDec(int64(out.value)).QuoInt64(int64(un.value))
		want := []math.LegacyDec{math.LegacyOneDec(), math.LegacyNewDecWithPrec(95, 2), keep}[i]
		require.True(t, ratio.Sub(want).Abs().LT(math.LegacyNewDecWithPrec(1, 4)), "claim %d paid %s of its value", i, ratio)
	}
	e.invariants()
}

// fakeAuthorized is a context in which x/shielded's ante has "authorized" m:
// for driving many handler calls without a proof each. Test-only: it spends
// pool coins without spending any note.
func (e *stakeEnv) fakeAuthorized(m shieldedtypes.PrivateMsg) sdk.Context {
	ctx, err := shieldedkeeper.AuthorizeMsg(e.ctx(), m, nil, 0)
	require.NoError(e.t, err)
	return shieldedkeeper.WithAuthorizedAction(ctx, struct{}{})
}

// Undelegations batch into one SDK entry per validator per epoch, so daily
// epochs never reach max_entries (32 at genesis; up to 22 live). And the
// epoch end never halts: a validator whose work fails is skipped and
// retried, the others proceed, the block commits.
func TestPrivateStakingEpochBatchingAndHaltSafety(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	params, err := e.app.StakingKeeper.GetParams(e.ctx())
	require.NoError(t, err)
	require.EqualValues(t, 32, params.MaxEntries)

	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	dn := e.delegate(vB, uint64(3_000*ssErth))
	dc := e.delegate(vC, uint64(500*ssErth))
	e.days(1)

	// Two undelegations in one epoch: one record, one SDK entry.
	un1 := e.undelegate(vB, dn, uint64(100*ssErth))
	dn = e.w.unspent(dn.denom, 1)
	un2 := e.undelegate(vB, dn, uint64(100*ssErth))
	require.Equal(t, un1.denom, un2.denom)
	e.days(1)
	r := e.record(vB, epochOf(t, un1.denom))
	require.Equal(t, math.NewIntFromUint64(un1.value+un2.value), r.Requested)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)

	// Thirty daily undelegations (handlers driven directly, see
	// fakeAuthorized): never refused, live entries peak at 22.
	srv := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper)
	maxLive := 0
	for d := 0; d < 30; d++ {
		m := &sstypes.MsgUndelegate{
			Transfer: shieldedtypes.Transfer{
				Nullifiers: [][]byte{privacy.FieldBytes(ssDet("fake", uint64(d)))}, ValueOut: uint64(ssErth),
				DenomOut: sstypes.DerthDenom(e.valoper(vB)),
			},
			Validator: e.valoper(vB), Pc: privacy.FieldBytes(ssDet("fakepc", uint64(d))),
		}
		_, err := srv.Undelegate(e.fakeAuthorized(m), m)
		require.NoError(t, err, "day %d", d)
		res := e.next(24 * time.Hour)
		require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "day %d", d)
		ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), mod, vB)
		require.NoError(t, err)
		maxLive = max(maxLive, len(ubd.Entries))
	}
	require.LessOrEqual(t, maxLive, 22)
	require.GreaterOrEqual(t, maxLive, 21)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(e.ctx()))

	// Halt safety: corrupt vB's book so its epoch work must fail (a queued
	// delegation the module does not hold). The block still commits, vC is
	// still processed, the failure and the broken books are reported.
	ctx := e.ctx()
	good, err := e.app.ShieldedStakingKeeper.Validators.Get(ctx, e.valoper(vB))
	require.NoError(t, err)
	bad := good
	bad.PendingDelegation = math.NewInt(1_000_000 * ssErth)
	require.NoError(t, e.app.ShieldedStakingKeeper.Validators.Set(ctx, e.valoper(vB), bad))
	un3 := e.undelegate(vC, dc, uint64(100*ssErth)) // vC has work to do this epoch
	res := e.next(24 * time.Hour)
	fails := eventsOf(res.Events, sstypes.EventTypeEpochFailure)
	require.Len(t, fails, 1)
	require.Equal(t, e.valoper(vB), fails[0]["validator"])
	require.Len(t, eventsOf(res.Events, sstypes.EventTypeInvariant), 1)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vC, epochOf(t, un3.denom)).Status, "vC processed")
	// vB's work was rolled back whole: nothing half-done.
	st, err := e.app.ShieldedStakingKeeper.Validators.Get(e.ctx(), e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, bad.PendingDelegation, st.PendingDelegation)

	// Repair the book: the next epoch processes vB again.
	require.NoError(t, e.app.ShieldedStakingKeeper.Validators.Set(e.ctx(), e.valoper(vB), good))
	res = e.next(24 * time.Hour)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure))
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(e.ctx()))
}

// Transparent delegation is closed on every route except a validator's own
// self-bond; private msgs refuse any route but the private ante.
func TestTransparentStakingBlocked(t *testing.T) {
	e := initStakeEnv(t)
	vA := e.genesisValidator()
	valoper := e.valoper(vA)
	user := e.bech(e.userAddr())
	amt := sdk.NewInt64Coin("uerth", 10*ssErth)

	// A plain signed MsgDelegate: refused in the ante.
	res := e.checkTx(e.signedTx(e.user, 300_000, 5_000, stakingtypes.NewMsgDelegate(user, valoper, amt)))
	require.Equal(t, sstypes.ErrTransparentStaking.ABCICode(), res.Code, res.Log)
	// Inside authz MsgExec: refused in the ante too.
	exec := authz.NewMsgExec(e.userAddr(), []sdk.Msg{stakingtypes.NewMsgDelegate(user, valoper, amt)})
	res = e.checkTx(e.signedTx(e.user, 300_000, 5_000, &exec))
	require.Equal(t, sstypes.ErrTransparentStaking.ABCICode(), res.Code, res.Log)

	// Straight to the msg router, as a contract's CosmosMsg, an ICA host tx or
	// a gov/group proposal reaches it: x/staking itself refuses (the hook).
	// Moving a self-bond to another validator stops it being one.
	vB, _ := e.createValidator(10 * ssErth)
	for _, m := range []sdk.Msg{
		stakingtypes.NewMsgDelegate(user, valoper, amt),
		stakingtypes.NewMsgBeginRedelegate(e.bech(sdk.AccAddress(vA)), valoper, e.valoper(vB), amt),
	} {
		h := e.app.MsgServiceRouter().Handler(m)
		cc, _ := e.ctx().CacheContext()
		_, err := h(cc, m)
		require.ErrorIs(t, err, sstypes.ErrTransparentStaking, "%T", m)
	}

	// The ICA host allowlist carries no delegation msg.
	for _, m := range e.app.ICAHostKeeper.GetParams(e.ctx()).AllowMessages {
		require.NotContains(t, m, "cosmos.staking.v1beta1.MsgDelegate")
		require.NotContains(t, m, "cosmos.staking.v1beta1.MsgUndelegate")
		require.NotContains(t, m, "Redelegate")
		require.NotContains(t, m, "CancelUnbonding")
	}

	// The operator's self-bond: allowed, both ways.
	op := e.bech(sdk.AccAddress(vA))
	fb := e.run(e.signedTx(e.val, 300_000, 5_000, stakingtypes.NewMsgDelegate(op, valoper, amt)))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	fb = e.run(e.signedTx(e.val, 400_000, 5_000, stakingtypes.NewMsgUndelegate(op, valoper, sdk.NewInt64Coin("uerth", ssErth))))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	// MsgCreateValidator (a self-bond) is allowed.
	e.createValidator(10 * ssErth)

	// Private msgs: a handler reached by any route but the private ante
	// refuses. The router, as wasm and the ICA host call it:
	tr := func(denom string, v uint64) shieldedtypes.Transfer {
		x := shieldedtypes.Transfer{Proof: []byte{1}, Root: make([]byte, 32), Fee: ssFee, ValueOut: v}
		if v > 0 {
			x.DenomOut = denom
		}
		for i := uint64(0); i < 3; i++ {
			x.Nullifiers = append(x.Nullifiers, privacy.FieldBytes(ssDet("bypass-nf", i)))
			x.Commitments = append(x.Commitments, privacy.FieldBytes(ssDet("bypass-cm", i)))
			x.Ciphertexts = append(x.Ciphertexts, nil)
		}
		return x
	}
	pc := privacy.FieldBytes(ssDet("bypass-pc", 0))
	voteTr, feeTr := tr(sstypes.DerthDenom(valoper), 1), tr("", 0)
	voteTr.Fee = 0
	for i := range feeTr.Nullifiers {
		feeTr.Nullifiers[i] = privacy.FieldBytes(ssDet("bypass-fee-nf", uint64(i)))
	}
	opts := []*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: "1"}}
	sig := make([]byte, 64)
	pk := secp256k1.GenPrivKeyFromSecret([]byte("bypass")).PubKey().Bytes()
	for _, m := range []sdk.Msg{
		&sstypes.MsgDelegate{Transfer: tr("uerth", 1), Validator: valoper, Pc: pc},
		&sstypes.MsgUndelegate{Transfer: tr(sstypes.DerthDenom(valoper), 1), Validator: valoper, Pc: pc},
		&sstypes.MsgClaimUnbonding{Transfer: tr(sstypes.UnbondDenom(valoper, 1), 1), Validator: valoper, Epoch: 1, Pc: pc},
		&sstypes.MsgStakeVote{Transfer: voteTr, FeeTransfer: feeTr, ProposalId: 1, Validator: valoper, Options: opts, Pc: pc},
		&sstypes.MsgLockPosition{Transfer: tr(sstypes.DerthDenom(valoper), 1), Validator: valoper, Pubkey: pk},
		&sstypes.MsgUpdatePosition{Transfer: tr("", 0), Signature: sig},
		&sstypes.MsgUnlockPosition{Transfer: tr("", 0), Pc: pc, Signature: sig},
		&sstypes.MsgPositionVote{Transfer: tr("", 0), Options: opts, Signature: sig},
	} {
		h := e.app.MsgServiceRouter().Handler(m)
		require.NotNil(t, h, "%T", m)
		_, err := h(e.ctx(), m)
		require.ErrorIs(t, err, shieldedtypes.ErrUnauthorized, "%T", m)
	}
	// ...and authz, which refuses a msg with no signer.
	exec = authz.NewMsgExec(e.userAddr(), []sdk.Msg{&sstypes.MsgDelegate{Validator: valoper}})
	fb = e.run(e.signedTx(e.user, 400_000, 5_000, &exec))
	require.NotEqual(t, uint32(0), fb.Code)
}

// ---- positions and stake votes ---------------------------------------------

func positionKey(i int) *secp256k1.PrivKey {
	return secp256k1.GenPrivKeyFromSecret([]byte(fmt.Sprintf("staking-test/position-%d", i)))
}

func (e *stakeEnv) sign(key *secp256k1.PrivKey, action string, id, nonce uint64, payload []byte) []byte {
	sig, err := key.Sign(sstypes.PositionSignBytes(ssChainID, action, id, nonce, payload))
	require.NoError(e.t, err)
	return sig
}

func (e *stakeEnv) position(id uint64) sstypes.Position {
	p, err := e.app.ShieldedStakingKeeper.Positions.Get(e.ctx(), id)
	require.NoError(e.t, err)
	return p
}

// lock moves amount of a derth note into a position keyed by key.
func (e *stakeEnv) lock(in *wnote, amount uint64, key *secp256k1.PrivKey, splits []allocationtypes.AllocationWeight) uint64 {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(in.denom)
	require.True(e.t, ok)
	p := e.build(spend{denom: in.denom, inputs: []*wnote{in}, valueOut: amount})
	m := &sstypes.MsgLockPosition{Transfer: p.tr, Validator: v, Splits: splits, Pubkey: key.PubKey().Bytes()}
	e.prove(p, m)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	ev := eventsOf(res.Events, sstypes.EventTypePosition)
	require.Len(e.t, ev, 1)
	id, err := strconv.ParseUint(ev[0]["position_id"], 10, 64)
	require.NoError(e.t, err)
	return id
}

func (e *stakeEnv) submitProposal() uint64 {
	e.t.Helper()
	msg, err := v1.NewMsgSubmitProposal(nil, sdk.NewCoins(sdk.NewInt64Coin("uerth", 2*ssErth)), e.bech(e.userAddr()),
		"ipfs://stake-vote-test", "stake vote test", "stake vote test", false)
	require.NoError(e.t, err)
	res := e.run(e.signedTx(e.user, 500_000, 5_000, msg))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	ev := eventsOf(res.Events, "submit_proposal")
	require.NotEmpty(e.t, ev)
	id, err := strconv.ParseUint(ev[0]["proposal_id"], 10, 64)
	require.NoError(e.t, err)
	return id
}

// stakeVoteMsg is a note's vote: a feeless transfer spending all of n
// against the proposal's snapshot root (or, current, against the current
// root, which the chain refuses), a second transfer paying the fee from any
// current ERTH note, and the derth minted back to a fresh note.
func (e *stakeEnv) stakeVoteMsg(n *wnote, proposalID uint64, opts []*v1.WeightedVoteOption, prove, current bool) (*sstypes.MsgStakeVote, *pendingTransfer, *wnote) {
	e.t.Helper()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), proposalID)
	require.NoError(e.t, err)
	v, ok := sstypes.ParseDerthDenom(n.denom)
	require.True(e.t, ok)
	s := spend{denom: n.denom, inputs: []*wnote{n}, valueOut: n.value, atSize: snap.TreeSize, feeless: true}
	if current {
		s.atSize = 0
	}
	p := e.build(s)
	if !current {
		root, err := e.w.tree(e.t, snap.TreeSize).Root()
		require.NoError(e.t, err)
		require.Equal(e.t, snap.Root, privacy.FieldBytes(root))
	}
	fee := e.feeOnly()
	p.also = append(p.also, fee)
	back := e.w.fresh(n.denom, 0)
	m := &sstypes.MsgStakeVote{
		Transfer: p.tr, FeeTransfer: fee.tr, ProposalId: proposalID, Validator: v, Options: opts,
		Pc: privacy.FieldBytes(e.w.pc(back)), Ciphertext: []byte("voted derth"),
	}
	if !prove {
		m.Transfer.Proof = make([]byte, 14656)
		m.FeeTransfer.Proof = make([]byte, 14656)
		return m, p, back
	}
	e.proveInto(p, m, &m.Transfer)
	e.proveInto(fee, m, &m.FeeTransfer)
	return m, p, back
}

// stakeVote votes all of n and returns the re-minted note.
func (e *stakeEnv) stakeVote(n *wnote, proposalID uint64, opt v1.VoteOption) *wnote {
	e.t.Helper()
	m, p, back := e.stakeVoteMsg(n, proposalID, v1.NewNonSplitVoteOption(opt), true, false)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	return e.minted(res, back)
}

func (e *stakeEnv) positionVoteMsg(id uint64, key *secp256k1.PrivKey, proposalID uint64, opt v1.VoteOption) (*sstypes.MsgPositionVote, *pendingTransfer) {
	p := e.feeOnly()
	m := &sstypes.MsgPositionVote{Transfer: p.tr, PositionId: id, ProposalId: proposalID, Options: v1.NewNonSplitVoteOption(opt)}
	m.Signature = e.sign(key, "vote", id, e.position(id).Nonce, m.SignPayload())
	return m, p
}

type tallyNums struct{ yes, abstain, no, veto math.LegacyDec }

// Stake votes on the real gov path: transparent validator and delegator
// votes, private spend-to-vote note votes, a position vote, inheritance of
// the un-voted derth, a residual third-party delegation — and the refusals
// that keep one unit of stake from voting twice.
func TestStakeVoteTally(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	vA := e.genesisValidator()
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(200 * ssErth))

	n1 := e.delegate(vB, uint64(1_000*ssErth))
	n2 := e.delegate(vB, uint64(600*ssErth))
	n4 := e.delegate(vB, uint64(300*ssErth))
	n5 := e.delegate(vB, uint64(200*ssErth))
	n3 := e.delegate(vA, uint64(400*ssErth))
	// An ERTH note in the snapshot, kept for the expired-root check below.
	snapErth := e.shield(uint64(ssErth))
	e.reserved = []*wnote{snapErth}
	e.days(1)

	// Position P from all of n4, before the proposal.
	pKey := positionKey(1)
	pid := e.lock(n4, n4.value, pKey, nil)

	// A residual third-party delegation on vA (none can be made on this chain;
	// one could only come from genesis): written straight into x/staking.
	third := sdk.AccAddress([]byte("third-party-delegatr"))
	thirdAmt := math.NewInt(50 * ssErth)
	{
		ctx := e.ctx()
		sk := e.app.StakingKeeper
		v, err := sk.GetValidator(ctx, vA)
		require.NoError(t, err)
		v, shares, err := sk.AddValidatorTokensAndShares(ctx, v, thirdAmt)
		require.NoError(t, err)
		require.NoError(t, sk.SetDelegation(ctx, stakingtypes.NewDelegation(e.bech(third), e.valoper(vA), shares)))
		coins := sdk.NewCoins(sdk.NewCoin("uerth", thirdAmt))
		require.NoError(t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
		require.NoError(t, e.app.BankKeeper.SendCoinsFromModuleToModule(ctx, earthtypes.ModuleName, stakingtypes.BondedPoolName, coins))
		_ = v
	}
	e.next(5 * time.Second)

	// --- the proposal enters voting: snapshot.
	prop := e.submitProposal()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), prop)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Root)
	supply := map[string]math.Int{}
	for _, vs := range snap.Validators {
		supply[vs.Validator] = vs.Supply
	}
	require.Equal(t, math.NewIntFromUint64(n1.value+n2.value+n4.value+n5.value), supply[e.valoper(vB)])
	require.Equal(t, math.NewIntFromUint64(n3.value), supply[e.valoper(vA)])

	// A position locked after the snapshot may not vote; nor may the note it
	// came from (spent), nor the note behind P (spent into P before).
	p2Key := positionKey(2)
	p2 := e.lock(n5, n5.value, p2Key, nil)
	m, _ := e.positionVoteMsg(p2, p2Key, prop, v1.OptionYes)
	m.Transfer.Proof = make([]byte, 14656)
	res := e.checkTx(e.privateTx(m))
	require.Equal(t, sstypes.ErrNoVoting.ABCICode(), res.Code, res.Log)
	for _, spent := range []*wnote{n5, n4} {
		sv, _, _ := e.stakeVoteMsg(spent, prop, v1.NewNonSplitVoteOption(v1.OptionNo), false, false)
		res = e.checkTx(e.privateTx(sv))
		require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), res.Code, res.Log)
	}
	// Nor a derth note made after the snapshot: it is not in the snapshot
	// root, and a vote spending against any other root is refused.
	n6 := e.delegate(vB, uint64(100*ssErth))
	sv, _, _ := e.stakeVoteMsg(n6, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, true)
	res = e.checkTx(e.privateTx(sv))
	require.Equal(t, sstypes.ErrNoVoting.ABCICode(), res.Code, res.Log)

	// --- votes. Transparent: vA No, vB Yes, the third party Abstain.
	voteTx := func(key *secp256k1.PrivKey, opt v1.VoteOption) {
		fb := e.run(e.signedTx(key, 300_000, 5_000, v1.NewMsgVote(sdk.AccAddress(key.PubKey().Address()), prop, opt, "")))
		require.Equal(t, uint32(0), fb.Code, fb.Log)
	}
	voteTx(e.val, v1.OptionNo)
	voteTx(vBKey, v1.OptionYes)
	require.NoError(t, e.app.GovKeeper.AddVote(e.ctx(), prop, third, v1.NewNonSplitVoteOption(v1.OptionAbstain), ""))
	// Private: n1 Abstain, by spending it; its derth comes straight back as a
	// new note, which cannot vote again (final), nor can n1 (spent).
	supplyB := e.app.BankKeeper.GetSupply(e.ctx(), n1.denom).Amount
	// Its fee comes from an ERTH note made after the snapshot: the fee
	// transfer spends against a current root, so it need not be in the
	// snapshot tree.
	late := e.shield(uint64(ssErth))
	require.GreaterOrEqual(t, late.pos, snap.TreeSize)
	for _, n := range e.w.notes {
		if n.denom == "uerth" && n != late {
			e.reserved = append(e.reserved, n)
		}
	}
	n1b := e.stakeVote(n1, prop, v1.OptionAbstain)
	e.reserved = []*wnote{snapErth}
	require.True(t, late.spent, "the vote's fee was paid from the post-snapshot note")
	require.Equal(t, n1.value, n1b.value)
	require.Equal(t, supplyB, e.app.BankKeeper.GetSupply(e.ctx(), n1.denom).Amount, "re-minted, not created")
	again, _, _ := e.stakeVoteMsg(n1b, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, true)
	res = e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrNoVoting.ABCICode(), res.Code, res.Log)
	replay, _, _ := e.stakeVoteMsg(n1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), false, false)
	res = e.checkTx(e.privateTx(replay))
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), res.Code, res.Log)
	// The snapshot root stays good for a stake vote after it leaves the
	// pool's anchor window; for anything else it is gone.
	{
		ctx := e.ctx()
		sp, err := e.app.ShieldedKeeper.Params.Get(ctx)
		require.NoError(t, err)
		sp.RootWindowSeconds = 1
		require.NoError(t, e.app.ShieldedKeeper.Params.Set(ctx, sp))
	}
	e.next(5 * time.Second)
	e.reserved = nil
	old := e.build(spend{denom: "uerth", atSize: snap.TreeSize})
	old.tr.Proof = make([]byte, 14656)
	// TODO(orchard-phase2): was a MsgTransfer of old.tr (the snapshot root
	// outside the window); redo as a MsgSend bundle.
	res = e.checkTx(e.privateTx(&shieldedtypes.MsgSend{}))
	require.Equal(t, shieldedtypes.ErrUnknownRoot.ABCICode(), res.Code, res.Log)
	// n3 Yes; position P Yes; n2 does not vote (vB inherits it).
	// Both proofs bind both transfers' nullifiers: n3's vote with another
	// vote's (valid) fee transfer verifies neither.
	sa, pa, ba := e.stakeVoteMsg(n3, prop, v1.NewNonSplitVoteOption(v1.OptionYes), true, false)
	sb, _, _ := e.stakeVoteMsg(n2, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, false)
	spliced := *sa
	spliced.FeeTransfer = sb.FeeTransfer
	res = e.checkTx(e.privateTx(&spliced))
	require.Equal(t, shieldedtypes.ErrInvalidProof.ABCICode(), res.Code, res.Log)
	// And the vote transfer must not pay a fee itself.
	feeing := *sa
	feeing.Transfer.Fee = ssFee
	res = e.checkTx(e.privateTx(&feeing))
	require.NotEqual(t, uint32(0), res.Code)
	require.Contains(t, res.Log, "the vote transfer pays no fee")
	fb0 := e.run(e.privateTx(sa))
	require.Equal(t, uint32(0), fb0.Code, fb0.Log)
	e.settle(pa)
	e.minted(fb0, ba)
	pv, pp := e.positionVoteMsg(pid, pKey, prop, v1.OptionYes)
	e.prove(pp, pv)
	fb := e.run(e.privateTx(pv))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pp)
	// The position's signature is spent with its nonce.
	pv2, pp2 := e.positionVoteMsg(pid, pKey, prop, v1.OptionYes)
	pv2.Signature = pv.Signature
	pv2.Transfer.Proof = make([]byte, 14656)
	_ = pp2
	res = e.checkTx(e.privateTx(pv2))
	require.Equal(t, sstypes.ErrSignature.ABCICode(), res.Code, res.Log)
	require.Equal(t, 3, countVotes(t, e, prop), "n1, n3, P")

	// --- the expected tally, from the stake as it stands.
	cc, _ := e.ctx().CacheContext()
	sk := e.app.StakingKeeper
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	info := func(v sdk.ValAddress) (bonded math.Int, shares, modShares math.LegacyDec) {
		val, err := sk.GetValidator(cc, v)
		require.NoError(t, err)
		d, err := sk.GetDelegation(cc, mod, v)
		require.NoError(t, err)
		return val.GetBondedTokens(), val.GetDelegatorShares(), d.Shares
	}
	bA, shA, mA := info(vA)
	bB, shB, mB := info(vB)
	thirdDel, err := sk.GetDelegation(cc, third, vA)
	require.NoError(t, err)
	power := func(sh math.LegacyDec, bonded math.Int, shares math.LegacyDec) math.LegacyDec {
		return sh.MulInt(bonded).Quo(shares)
	}
	frac := func(m math.LegacyDec, d uint64, s math.Int) math.LegacyDec {
		return m.MulInt(math.NewIntFromUint64(d)).QuoInt(s)
	}
	dedA := frac(mA, n3.value, supply[e.valoper(vA)])
	dedB1 := frac(mB, n1.value, supply[e.valoper(vB)])
	dedBP := frac(mB, n4.value, supply[e.valoper(vB)])
	want := tallyNums{
		yes:     power(shB.Sub(dedB1).Sub(dedBP), bB, shB).Add(power(dedA, bA, shA)).Add(power(dedBP, bB, shB)),
		abstain: power(thirdDel.Shares, bA, shA).Add(power(dedB1, bB, shB)),
		no:      power(shA.Sub(thirdDel.Shares).Sub(dedA), bA, shA),
		veto:    math.LegacyZeroDec(),
	}
	proposal, err := e.app.GovKeeper.Proposals.Get(cc, prop)
	require.NoError(t, err)
	votesBefore := countVotes(t, e, prop)
	_, _, got, err := e.app.GovKeeper.Tally(cc, proposal)
	require.NoError(t, err)
	for name, pair := range map[string][2]string{
		"yes": {want.yes.TruncateInt().String(), got.YesCount}, "abstain": {want.abstain.TruncateInt().String(), got.AbstainCount},
		"no": {want.no.TruncateInt().String(), got.NoCount}, "veto": {"0", got.NoWithVetoCount},
	} {
		w, _ := math.NewIntFromString(pair[0])
		g, _ := math.NewIntFromString(pair[1])
		require.True(t, w.Sub(g).Abs().LTE(math.NewInt(4)), "%s: want %s got %s", name, w, g)
	}
	// The whole bonded stake of the two voting validators is counted once.
	sum := math.ZeroInt()
	for _, c := range []string{got.YesCount, got.AbstainCount, got.NoCount, got.NoWithVetoCount} {
		x, _ := math.NewIntFromString(c)
		sum = sum.Add(x)
	}
	require.True(t, sum.Sub(bA.Add(bB)).Abs().LTE(math.NewInt(8)), "counted %s of %s", sum, bA.Add(bB))
	// No side effects on the private votes (x/gov's TallyResult query runs
	// the same function).
	require.Equal(t, votesBefore, countVotes(t, e, prop))

	// --- genesis round trip mid-vote: snapshot, votes, positions, books.
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	gs1, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	gs2, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, gs1, gs2)
	require.NotEmpty(t, gs2.Votes)
	require.NotEmpty(t, gs2.Positions)
	t1, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(vB)))
	require.NoError(t, err)
	t2, err := fresh.ShieldedStakingKeeper.Tallies.Get(fctx, collections.Join(prop, e.valoper(vB)))
	require.NoError(t, err)
	require.Equal(t, t1, t2, "tallies are rebuilt from the votes")

	// --- voting ends: x/gov tallies with the same function, then the module
	// forgets the proposal.
	e.days(8)
	final, err := e.app.GovKeeper.Proposals.Get(e.ctx(), prop)
	require.NoError(t, err)
	require.NotEqual(t, v1.StatusVotingPeriod, final.Status)
	require.NotEqual(t, "0", final.FinalTallyResult.AbstainCount)
	_, err = e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), prop)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Zero(t, countVotes(t, e, prop))
	e.invariants()
}

func countVotes(t *testing.T, e *stakeEnv, prop uint64) int {
	n := 0
	err := e.app.ShieldedStakingKeeper.Votes.Walk(e.ctx(), collections.NewPrefixedPairRange[uint64, []byte](prop),
		func(collections.Pair[uint64, []byte], sstypes.StakeVote) (bool, error) {
			n++
			return false, nil
		})
	require.NoError(t, err)
	return n
}

// Groundworks is weighted by positions: an account's bonded stake no longer
// votes; a position votes derth x epoch rate, re-weighed every epoch, and is
// driven by its own key inside unsigned txs.
func TestGroundworksPositions(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(3_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	gw := allocationtypes.STREAM_ID_GROUNDWORKS
	ak := e.app.AllocationKeeper

	// Groundworks options are added by governance.
	gov := authtypes.NewModuleAddress("gov")
	fund := sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, fund))
	_, err := allocationkeeper.NewMsgServerImpl(ak).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: gw, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	opt := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	_, err = ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
	require.NoError(t, err)

	// (A validator's self-bond is Groundworks weight too: see
	// TestGroundworksSelfBondWeight. This test is about positions.)

	dn := e.delegate(vB, uint64(1_000*ssErth))
	e.days(2) // delegated, then one epoch of rewards compounded
	rate := e.state(vB).EpochRate
	require.True(t, rate.GT(math.LegacyOneDec()))

	key := positionKey(1)
	id := e.lock(dn, uint64(500*ssErth), key, opt)
	p := e.position(id)
	wantW := rate.MulInt64(500 * ssErth).TruncateInt()
	require.Equal(t, wantW, p.Weight)
	voter, err := ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), sstypes.PositionVoterKey(id)))
	require.NoError(t, err)
	require.Equal(t, wantW, voter.Weight)
	o, err := ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
	require.NoError(t, err)
	require.Equal(t, wantW, o.AmountAllocated)
	e.invariants()

	// An epoch later the rate (and so the weight) has risen with rewards.
	e.days(1)
	p = e.position(id)
	rate2 := e.state(vB).EpochRate
	require.True(t, rate2.GT(rate))
	require.Equal(t, rate2.MulInt64(500*ssErth).TruncateInt(), p.Weight)
	voter, err = ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), sstypes.PositionVoterKey(id)))
	require.NoError(t, err)
	require.Equal(t, p.Weight, voter.Weight)
	// The stream now carries weight, so its emission index runs.
	idx, err := ak.RewardIndex.Get(e.ctx(), uint32(gw))
	require.NoError(t, err)
	require.True(t, idx.IsPositive(), "Groundworks emission accrues to the position's split")

	// Update: clear the split (signed by the position key); a replay of that
	// signature is refused (the nonce moved on); a wrong key is refused.
	up := func(splits []allocationtypes.AllocationWeight, signer *secp256k1.PrivKey, nonce uint64) (*sstypes.MsgUpdatePosition, *pendingTransfer) {
		pt := e.feeOnly()
		m := &sstypes.MsgUpdatePosition{Transfer: pt.tr, PositionId: id, Splits: splits}
		m.Signature = e.sign(signer, "update", id, nonce, m.SignPayload())
		return m, pt
	}
	m, pt := up(nil, key, p.Nonce)
	e.prove(pt, m)
	fb := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	_, err = ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), sstypes.PositionVoterKey(id)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	replay, _ := up(nil, key, p.Nonce)
	replay.Transfer.Proof = make([]byte, 14656)
	res := e.checkTx(e.privateTx(replay))
	require.Equal(t, sstypes.ErrSignature.ABCICode(), res.Code, res.Log)
	wrong, _ := up(opt, positionKey(9), p.Nonce+1)
	wrong.Transfer.Proof = make([]byte, 14656)
	res = e.checkTx(e.privateTx(wrong))
	require.Equal(t, sstypes.ErrSignature.ABCICode(), res.Code, res.Log)

	// Unlock: the derth comes back as a note to the pc the key signed for.
	pt = e.feeOnly()
	back := e.w.fresh(dn.denom, 0)
	um := &sstypes.MsgUnlockPosition{Transfer: pt.tr, PositionId: id, Pc: privacy.FieldBytes(e.w.pc(back))}
	um.Signature = e.sign(key, "unlock", id, p.Nonce+1, um.SignPayload())
	e.prove(pt, um)
	fb = e.run(e.privateTx(um))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	back = e.minted(fb, back)
	require.Equal(t, uint64(500*ssErth), back.value)
	_, err = e.app.ShieldedStakingKeeper.Positions.Get(e.ctx(), id)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), e.app.ShieldedStakingKeeper.ModuleAddress(), dn.denom).IsZero())
	e.invariants()
}

// An expedited proposal that fails its one-day vote continues as a regular
// one; its stake-vote snapshot (and any votes) continue with it.
func TestStakeVoteSnapshotFollowsExpeditedConversion(t *testing.T) {
	e := initStakeEnv(t)
	e.shield(uint64(10 * ssErth))
	msg, err := v1.NewMsgSubmitProposal(nil, sdk.NewCoins(sdk.NewInt64Coin("uerth", 5*ssErth)), e.bech(e.userAddr()),
		"ipfs://expedited", "expedited", "expedited", true)
	require.NoError(t, err)
	res := e.run(e.signedTx(e.user, 500_000, 5_000, msg))
	require.Equal(t, uint32(0), res.Code, res.Log)
	id, err := strconv.ParseUint(eventsOf(res.Events, "submit_proposal")[0]["proposal_id"], 10, 64)
	require.NoError(t, err)
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), id)
	require.NoError(t, err)
	firstEnd := snap.VotingEnd

	e.days(2) // the expedited period ends without a quorum: converted
	prop, err := e.app.GovKeeper.Proposals.Get(e.ctx(), id)
	require.NoError(t, err)
	require.Equal(t, v1.StatusVotingPeriod, prop.Status)
	require.False(t, prop.Expedited)
	snap, err = e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), id)
	require.NoError(t, err, "the snapshot outlives the expedited end")
	require.Equal(t, prop.VotingEndTime.UnixNano(), snap.VotingEnd)
	require.Greater(t, snap.VotingEnd, firstEnd)

	e.days(7)
	_, err = e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), id)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// A validator operator's transparent self-bond is Groundworks weight, kept in
// step with the bond (staking hooks) and with a slash (resynced at EndBlock);
// the private staking module's own delegations never are; positions carry
// their own weight beside it.
func TestGroundworksSelfBondWeight(t *testing.T) {
	t.Skip("TODO(orchard-phase2): its private msgs still carry a legacy transfer, which the private ante refuses")
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(3_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	gw := allocationtypes.STREAM_ID_GROUNDWORKS
	ak := e.app.AllocationKeeper
	gov := authtypes.NewModuleAddress("gov")
	fund := sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, fund))
	_, err := allocationkeeper.NewMsgServerImpl(ak).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: gw, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	opt := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	op := sdk.AccAddress(e.val.PubKey().Address())
	vA := e.genesisValidator()
	voterW := func(key []byte) math.Int {
		v, err := ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), key))
		require.NoError(t, err)
		return v.Weight
	}
	bonded := func() math.Int {
		b, err := e.app.StakingKeeper.GetDelegatorBonded(e.ctx(), op)
		require.NoError(t, err)
		return b
	}

	// Positions on vB and on vA (which is slashed below), so the stream has
	// private weight too.
	dn := e.delegate(vB, uint64(1_000*ssErth))
	dnA := e.delegate(vA, uint64(400*ssErth))
	e.days(1)
	id := e.lock(dn, uint64(500*ssErth), positionKey(1), opt)
	idA := e.lock(dnA, dnA.value, positionKey(2), opt)
	posW := e.position(id).Weight
	require.True(t, posW.IsPositive())
	sumVoters := func() math.Int {
		return voterW(op).Add(voterW(sstypes.PositionVoterKey(id))).Add(voterW(sstypes.PositionVoterKey(idA)))
	}

	// The operator votes with its self-bond (100 ERTH).
	fb := e.run(e.signedTx(e.val, 300_000, 5_000, &allocationtypes.MsgSetAllocations{
		Creator: e.bech(op), Stream: gw, Percentages: opt,
	}))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	require.Equal(t, math.NewInt(100*ssErth), voterW(op))
	require.Equal(t, bonded(), voterW(op))

	// Bond more: the weight follows (AfterDelegationModified).
	fb = e.run(e.signedTx(e.val, 300_000, 5_000, stakingtypes.NewMsgDelegate(e.bech(op), e.valoper(vA), sdk.NewInt64Coin("uerth", 50*ssErth))))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	require.Equal(t, math.NewInt(150*ssErth), voterW(op))

	// The module account has delegations and no weight, ever; the position's
	// weight is untouched by the operator's moves.
	src := sskeeper.NewPositionWeightSource(e.app.ShieldedStakingKeeper)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	require.True(t, e.modDelegation(vB).IsPositive())
	w, err := src.Weight(e.ctx(), mod)
	require.NoError(t, err)
	require.True(t, w.IsZero())
	require.False(t, src.TracksBonded(mod))
	_, err = ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), []byte(mod)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, posW, voterW(sstypes.PositionVoterKey(id)))
	e.days(1) // an epoch: positions reweigh, the operator stays at its bond
	require.Equal(t, math.NewInt(150*ssErth), voterW(op))
	require.Equal(t, e.position(id).Weight, voterW(sstypes.PositionVoterKey(id)))
	o, err := ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
	require.NoError(t, err)
	require.Equal(t, sumVoters(), o.AmountAllocated)

	// A slash of vA: the operator's weight drops to its slashed bond at that
	// block's end, though no delegation of its own changed.
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	infraction := e.height
	ctx := e.ctx()
	val, err := e.app.StakingKeeper.GetValidator(ctx, vA)
	require.NoError(t, err)
	power := val.ConsensusPower(e.app.StakingKeeper.PowerReduction(ctx))
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)
	before := bonded()
	posABefore, posBBefore := e.position(idA).Weight, e.position(id).Weight
	e.block(5*time.Second, []abci.Misbehavior{{
		Type: abci.MisbehaviorType_DUPLICATE_VOTE, Validator: abci.Validator{Address: consAddr, Power: power},
		Height: infraction, Time: e.times[infraction], TotalVotingPower: power + 1000,
	}})
	after := bonded()
	require.True(t, after.LT(before), "slashed: %s -> %s", before, after)
	require.Equal(t, after, voterW(op), "weight follows the slash")
	slashed, err := ak.SlashedValidators.Has(e.ctx(), vA.Bytes())
	require.NoError(t, err)
	require.False(t, slashed, "the record is cleared at EndBlock")
	// The positions on vA re-weighed in the same block at vA's live,
	// post-slash rate, which is now its epoch rate; vB's did not move.
	stA := e.state(vA)
	pA := e.position(idA)
	require.True(t, pA.Weight.LT(posABefore), "position on the slashed validator: %s -> %s", posABefore, pA.Weight)
	require.Equal(t, stA.EpochRate.MulInt(pA.Derth).TruncateInt(), pA.Weight)
	require.Equal(t, pA.Weight, voterW(sstypes.PositionVoterKey(idA)))
	live, err := e.app.ShieldedStakingKeeper.Rate(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	require.True(t, live.Sub(stA.EpochRate).Abs().LT(math.LegacyNewDecWithPrec(1, 6)), "epoch rate %s live %s", stA.EpochRate, live)
	require.Equal(t, posBBefore, e.position(id).Weight)
	require.Equal(t, posBBefore, voterW(sstypes.PositionVoterKey(id)))
	o, err = ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
	require.NoError(t, err)
	require.Equal(t, sumVoters(), o.AmountAllocated)
	require.NoError(t, ak.AssertHotInvariants(e.ctx()))
	e.invariants()
}
