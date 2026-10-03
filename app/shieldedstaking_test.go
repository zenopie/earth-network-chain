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
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
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
	dextypes "github.com/earth-network/earth/x/dex/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// ---- private msgs, built and proven against the chain as it stands --------

// delegateMsg stakes amount out of the ERTH note in (its bundle releases
// amount and the fee); the derth is minted to a fresh stake note.
func (e *stakeEnv) delegateMsg(val sdk.ValAddress, in *wnote, amount uint64) (*sstypes.MsgDelegate, *pendingBundle, *snote) {
	p := e.build(spend{denom: "uerth", inputs: []*wnote{in}, valueOut: amount})
	v := e.valoper(val)
	dn := e.freshStake(sstypes.DerthDenom(v), 0)
	sp := e.stake(&stakePlan{denom: sstypes.DerthDenom(v), mint: dn})
	m := &sstypes.MsgDelegate{Bundle: p.b, Amount: amount, Validator: v, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	return m, p, dn
}

// delegate stakes amount out of an ERTH note and returns the derth stake note.
func (e *stakeEnv) delegate(val sdk.ValAddress, amount uint64) *snote {
	e.t.Helper()
	in := e.w.unspent("uerth", amount)
	require.NotNil(e.t, in)
	m, p, dn := e.delegateMsg(val, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	return e.mintedStake(res, dn)
}

// undelegateMsg undelegates amount of the stake note in, its change back as
// a new stake note; the claim is minted to a fresh stake note.
func (e *stakeEnv) undelegateMsg(val sdk.ValAddress, in *snote, amount uint64) (*sstypes.MsgUndelegate, *pendingBundle, *stakePlan, *snote) {
	epoch, err := e.app.ShieldedStakingKeeper.Epoch.Get(e.ctx())
	require.NoError(e.t, err)
	p := e.feeOnly()
	v := e.valoper(val)
	un := e.freshStake(sstypes.UnbondDenom(v, epoch.Number), 0)
	sp := e.stake(&stakePlan{denom: in.denom, ins: []*snote{in}, outs: []*snote{e.freshStake(in.denom, in.amount-amount)},
		vOut: amount, mint: un})
	m := &sstypes.MsgUndelegate{Bundle: p.b, Validator: v, Amount: amount, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	return m, p, sp, un
}

// undelegate turns amount of a derth stake note into an unbond claim note.
func (e *stakeEnv) undelegate(val sdk.ValAddress, in *snote, amount uint64) *snote {
	e.t.Helper()
	m, p, sp, un := e.undelegateMsg(val, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	return e.mintedStake(res, un)
}

func (e *stakeEnv) claimMsg(in *snote) (*sstypes.MsgClaimUnbonding, *pendingBundle, *stakePlan, *wnote) {
	return e.claimMsgFee(in, 0, true)
}

// claimMsgFee is a claim of all of in paying feeFromOutput out of what it
// claims (no bundle), or a fee bundle when it is 0.
func (e *stakeEnv) claimMsgFee(in *snote, feeFromOutput uint64, prove bool) (*sstypes.MsgClaimUnbonding, *pendingBundle, *stakePlan, *wnote) {
	v, epoch, ok := sstypes.ParseUnbondDenom(in.denom)
	require.True(e.t, ok)
	out := e.w.fresh("uerth", 0)
	sp := e.stake(&stakePlan{denom: in.denom, ins: []*snote{in}, vOut: in.amount})
	m := &sstypes.MsgClaimUnbonding{Validator: v, Epoch: epoch, Amount: in.amount, Pc: privacy.FieldBytes(e.w.pc(out)),
		Ciphertext: shieldedtest.BlindCT(fmt.Sprintf("claim/%d", e.w.seq)), FeeFromOutput: feeFromOutput, Stake: sp.proof}
	var p *pendingBundle
	if feeFromOutput == 0 {
		p = e.feeOnly()
		m.Bundle = &p.b
	}
	if !prove {
		unproven(m)
		return m, p, sp, out
	}
	if p != nil {
		e.prove(m, p)
	}
	e.proveStake(m, sp)
	return m, p, sp, out
}

// claim spends an unbond note and returns the ERTH note (nil if it paid 0).
func (e *stakeEnv) claim(in *snote) *wnote {
	e.t.Helper()
	m, p, sp, out := e.claimMsg(in)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	for _, ev := range eventsOf(res.Events, sstypes.EventTypeClaim) {
		if ev["amount"] == "0" {
			return nil
		}
	}
	return e.minted(res, out)
}

func (e *stakeEnv) feeOnly() *pendingBundle { return e.build(spend{denom: "uerth"}) }

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
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()

	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth)) // fees

	// --- delegate 2,000 ERTH: rate 1, so 2,000 derth; the ERTH waits for the
	// epoch in the module account.
	dn := e.delegate(vB, uint64(2_000*ssErth))
	require.Equal(t, uint64(2_000*ssErth), dn.amount)
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
	require.GreaterOrEqual(t, int64(un.amount), atRead)
	require.InEpsilon(t, atRead, int64(un.amount), 1e-3)
	ep := epochOf(t, un.denom)
	require.Equal(t, sstypes.UNBOND_STATUS_PENDING, e.record(vB, ep).Status)
	// The remaining 1,000 derth stays a stake note (change of the
	// undelegate), the owner's own.
	require.Equal(t, uint64(1_000*ssErth), e.stakeBalance(sstypes.DerthDenom(e.valoper(vB))))
	e.invariants()

	// A claim before maturity is refused before anything is spent.
	cm, _, _, _ := e.claimMsg(un)
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
	want := math.NewIntFromUint64(un.amount).Mul(r.Payout).Quo(r.Requested)
	// A fee from output must leave something to mint, and must clear the
	// fee floor like any other: both refused before anything is spent.
	big, _, _, _ := e.claimMsgFee(un, want.Uint64(), false)
	res = e.checkTx(e.privateTx(big))
	require.Equal(t, sstypes.ErrAmount.ABCICode(), res.Code, res.Log)
	tiny, _, _, _ := e.claimMsgFee(un, 1, false)
	res = e.checkTx(e.privateTx(tiny))
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), res.Code, res.Log)
	before := e.w.balance("uerth")
	fm, _, fsp, out := e.claimMsgFee(un, ssFee, true)
	require.Empty(t, fm.PrivateBundles(), "a claim paying from its output carries no bundle")
	cres := e.run(e.privateTx(fm))
	require.Equal(t, uint32(0), cres.Code, cres.Log)
	e.settleStake(fsp)
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
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	dn := e.delegate(vB, uint64(2_000*ssErth))
	e.days(1)

	// E1: before the infraction.
	un1 := e.undelegate(vB, dn, uint64(500*ssErth))
	dn = e.unspentStake(dn.denom)
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
	dn = e.unspentStake(dn.denom)
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
	m := &sstypes.MsgDelegate{Bundle: p.b, Validator: e.valoper(vB), Amount: uint64(100 * ssErth),
		Stake: e.stake(&stakePlan{denom: sstypes.DerthDenom(e.valoper(vB)), mint: e.freshStake(sstypes.DerthDenom(e.valoper(vB)), 0)}).proof}
	unproven(m) // refused before any proof is read
	ct := e.checkTx(e.privateTx(m))
	require.Equal(t, sstypes.ErrValidator.ABCICode(), ct.Code, ct.Log)
	require.Contains(t, ct.Log, "jailed")
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), m.Bundle.Actions[0].Nullifier)
	require.NoError(t, err)
	require.False(t, spent)

	// Epoch end: the pending record undelegates its reduced target (the
	// jailed validator still releases stake). Then everything matures.
	e.days(1)
	r3 = e.record(vB, epochOf(t, un3.denom))
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, r3.Status)
	e.invariants()
	e.days(22)
	for _, un := range []*snote{un1, un2, un3} {
		require.Equal(t, sstypes.UNBOND_STATUS_MATURED, e.record(vB, epochOf(t, un.denom)).Status)
	}
	e.invariants()

	// Claims: E1 in full, E2 at 95%, E3 at 1 - f.
	for i, un := range []*snote{un1, un2, un3} {
		out := e.claim(un)
		require.NotNil(t, out)
		ratio := math.LegacyNewDec(int64(out.value)).QuoInt64(int64(un.amount))
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
	dn = e.unspentStake(dn.denom)
	un2 := e.undelegate(vB, dn, uint64(100*ssErth))
	require.Equal(t, un1.denom, un2.denom)
	e.days(1)
	r := e.record(vB, epochOf(t, un1.denom))
	require.Equal(t, math.NewIntFromUint64(un1.amount+un2.amount), r.Requested)
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
			Validator: e.valoper(vB), Amount: uint64(ssErth),
			Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("fakepc", uint64(d))), SpcCiphertext: shieldedtest.BlindCT("spc")},
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
	// tr is a well-formed (unproven) bundle releasing v of denom beside the
	// fee (fee-only for denom "").
	tr := func(label, denom string, v, fee uint64) shieldedtypes.Bundle {
		b := stubFeeBundle(1)
		b.Balances = nil
		if fee > 0 {
			b.Balances = append(b.Balances, shieldedtypes.ValueBalance{Denom: "uerth", Amount: fee})
		}
		if v > 0 {
			b.Balances = append(b.Balances, shieldedtypes.ValueBalance{Denom: denom, Amount: v})
		}
		for i := range b.Actions {
			b.Actions[i].Nullifier = privacy.FieldBytes(ssDet("bypass-nf/"+label, uint64(i)))
		}
		return b
	}
	pc := privacy.FieldBytes(ssDet("bypass-pc", 0))
	opts := []*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: "1"}}
	z := make([]byte, 32)
	st := sstypes.StakeProof{Proof: make([]byte, shieldedtypes.ProofBytes), Anchor: z, Nullifiers: [][]byte{privacy.FieldBytes(ssDet("bypass-snf", 0)), z},
		Commitments: [][]byte{z, z}, Ciphertexts: [][]byte{nil, nil}, SpcMint: pc, OwnerTag: pc}
	claimFee := tr("c", "", 0, ssFee)
	none := sstypes.StakeProof{Proof: make([]byte, shieldedtypes.ProofBytes), Anchor: z, Nullifiers: [][]byte{z, z}, Commitments: [][]byte{z, z}, Ciphertexts: [][]byte{nil, nil}, SpcMint: pc, OwnerTag: pc}
	restake := st
	restake.Commitments = [][]byte{pc, z}
	restake.Ciphertexts = [][]byte{[]byte("restake-ct"), nil}
	// The msgs that mint a stake note carry its blind ciphertext.
	stMint, noneMint := st, none
	stMint.SpcCiphertext, noneMint.SpcCiphertext = shieldedtest.BlindCT("m"), shieldedtest.BlindCT("m")
	for _, m := range []sdk.Msg{
		&sstypes.MsgDelegate{Bundle: tr("d", "uerth", 0, ssFee+1), Amount: 1, Validator: valoper, Stake: noneMint},
		&sstypes.MsgRestake{Bundle: tr("r", "", 0, ssFee), Validator: valoper, Stake: restake},
		&sstypes.MsgUndelegate{Bundle: tr("u", "", 0, ssFee), Validator: valoper, Amount: 1, Stake: stMint},
		&sstypes.MsgClaimUnbonding{Bundle: &claimFee, Validator: valoper, Epoch: 1, Amount: 1, Pc: pc,
			Ciphertext: shieldedtest.BlindCT("c"), Stake: st},
		&sstypes.MsgStakeVote{Bundle: tr("v", "", 0, ssFee), ProposalId: 1, Validator: valoper, Options: opts,
			Weight: 1, Stake: stMint},
		&sstypes.MsgLockPosition{Bundle: tr("l", "", 0, ssFee), Validator: valoper, Amount: 1, Stake: st},
		&sstypes.MsgUpdatePosition{Bundle: tr("up", "", 0, ssFee), Stake: none},
		&sstypes.MsgUnlockPosition{Bundle: tr("ul", "", 0, ssFee), Stake: noneMint},
		&sstypes.MsgPositionVote{Bundle: tr("pv", "", 0, ssFee), Options: opts, Stake: none},
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

// positionKey is the owner-tag salt of a test position: its owner proves
// H(TAG_OTAG, owner_pk, salt) again to act on it.
func positionKey(i int) fr.Element { return ssDet("position-salt", uint64(i)) }

func (e *stakeEnv) position(id uint64) sstypes.Position {
	res, err := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Position(e.ctx(), &sstypes.QueryPositionRequest{Id: id})
	require.NoError(e.t, err)
	return res.Position
}

// lock moves amount of a derth stake note into a position owned by (the
// wallet, salt), the change back as a stake note.
func (e *stakeEnv) lock(in *snote, amount uint64, salt fr.Element, splits []allocationtypes.AllocationWeight) uint64 {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(in.denom)
	require.True(e.t, ok)
	p := e.feeOnly()
	sp := e.stake(&stakePlan{denom: in.denom, ins: []*snote{in}, outs: []*snote{e.freshStake(in.denom, in.amount-amount)},
		vOut: amount, salt: salt})
	m := &sstypes.MsgLockPosition{Bundle: p.b, Validator: v, Amount: amount, Splits: splits, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	ev := eventsOf(res.Events, sstypes.EventTypePosition)
	require.Len(e.t, ev, 1)
	id, err := strconv.ParseUint(ev[0]["position_id"], 10, 64)
	require.NoError(e.t, err)
	return id
}

// ownerProof is a stake proof spending and creating nothing: it shows the
// prover owns (wallet, salt), and names a stake pc of the same owner (mint).
func (e *stakeEnv) ownerProof(salt fr.Element, mint *snote) *stakePlan {
	return e.stake(&stakePlan{salt: salt, mint: mint})
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

// stakeVoteMsg is a stake note's vote: the stake proof spends all of n
// against the proposal's snapshot stake root (or, current, against the
// current one, which the chain refuses), a fee bundle pays the fee from any
// current ERTH note, and the derth is re-minted to a fresh stake note of the
// same owner.
func (e *stakeEnv) stakeVoteMsg(n *snote, proposalID uint64, opts []*v1.WeightedVoteOption, prove, current bool) (*sstypes.MsgStakeVote, *pendingBundle, *stakePlan, *snote) {
	e.t.Helper()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), proposalID)
	require.NoError(e.t, err)
	v, ok := sstypes.ParseDerthDenom(n.denom)
	require.True(e.t, ok)
	back := e.freshStake(n.denom, 0)
	plan := &stakePlan{denom: n.denom, ins: []*snote{n}, vOut: n.amount, mint: back, atSize: snap.TreeSize}
	if current {
		plan.atSize = 0
	}
	sp := e.stake(plan)
	if !current {
		require.Equal(e.t, snap.Root, sp.proof.Anchor)
	}
	fee := e.feeOnly()
	m := &sstypes.MsgStakeVote{Bundle: fee.b, ProposalId: proposalID, Validator: v, Options: opts,
		Weight: n.amount, Stake: sp.proof}
	if !prove {
		unproven(m)
		return m, fee, sp, back
	}
	e.prove(m, fee)
	e.proveStake(m, sp)
	return m, fee, sp, back
}

// stakeVote votes all of n and returns the re-minted note.
func (e *stakeEnv) stakeVote(n *snote, proposalID uint64, opt v1.VoteOption) *snote {
	e.t.Helper()
	m, p, sp, back := e.stakeVoteMsg(n, proposalID, v1.NewNonSplitVoteOption(opt), true, false)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	return e.mintedStake(res, back)
}

// positionVoteMsg votes position id as the owner of salt (proven).
func (e *stakeEnv) positionVoteMsg(id uint64, salt fr.Element, proposalID uint64, opt v1.VoteOption) (*sstypes.MsgPositionVote, *pendingBundle, *stakePlan) {
	p := e.feeOnly()
	sp := e.ownerProof(salt, nil)
	m := &sstypes.MsgPositionVote{Bundle: p.b, PositionId: id, ProposalId: proposalID,
		Options: v1.NewNonSplitVoteOption(opt), Stake: sp.proof}
	return m, p, sp
}

type tallyNums struct{ yes, abstain, no, veto math.LegacyDec }

// Stake votes on the real gov path: transparent validator and delegator
// votes, private spend-to-vote note votes, a position vote, inheritance of
// the un-voted derth, a residual third-party delegation — and the refusals
// that keep one unit of stake from voting twice.
func TestStakeVoteTally(t *testing.T) {
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
	pid := e.lock(n4, n4.amount, pKey, nil)

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
	supplyAt := func(v sdk.ValAddress) math.Int {
		s, err := e.app.ShieldedStakingKeeper.SnapshotSupply(e.ctx(), prop, e.valoper(v))
		require.NoError(t, err)
		return s
	}
	require.Empty(t, snap.Validators, "snapshots are O(1): supplies are read lazily")
	require.Equal(t, math.NewIntFromUint64(n1.amount+n2.amount+n4.amount+n5.amount), supplyAt(vB))
	require.Equal(t, math.NewIntFromUint64(n3.amount), supplyAt(vA))
	supply := map[string]math.Int{e.valoper(vA): supplyAt(vA), e.valoper(vB): supplyAt(vB)}

	// A position locked after the snapshot may not vote; nor may the note it
	// came from (spent), nor the note behind P (spent into P before).
	p2Key := positionKey(2)
	p2 := e.lock(n5, n5.amount, p2Key, nil)
	m, _, _ := e.positionVoteMsg(p2, p2Key, prop, v1.OptionYes)
	unproven(m)
	res := e.checkTx(e.privateTx(m))
	require.Equal(t, sstypes.ErrNoVoting.ABCICode(), res.Code, res.Log)
	for _, spent := range []*snote{n5, n4} {
		sv, _, _, _ := e.stakeVoteMsg(spent, prop, v1.NewNonSplitVoteOption(v1.OptionNo), false, false)
		res = e.checkTx(e.privateTx(sv))
		require.Equal(t, sstypes.ErrStakeNullifierSpent.ABCICode(), res.Code, res.Log)
	}
	// Nor a derth note made after the snapshot: it is not in the snapshot
	// root, and a vote spending against any other root is refused.
	n6 := e.delegate(vB, uint64(100*ssErth))
	// The supply checkpoint keeps vB's snapshot supply as it was.
	require.Equal(t, math.NewIntFromUint64(n1.amount+n2.amount+n4.amount+n5.amount), supplyAt(vB))
	require.True(t, e.app.ShieldedStakingKeeper.Supply(e.ctx(), e.valoper(vB)).GT(supplyAt(vB)))
	sv, _, _, _ := e.stakeVoteMsg(n6, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, true)
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
	// new stake note of the same owner, which cannot vote again (final), nor
	// can n1 (spent).
	supplyB := e.app.ShieldedStakingKeeper.Supply(e.ctx(), e.valoper(vB))
	// Its fee comes from an ERTH note made after the snapshot: the fee
	// bundle spends against the pool's current roots, so it need not be in
	// any snapshot.
	late := e.shield(uint64(ssErth))
	for _, n := range e.w.notes {
		if n.denom == "uerth" && n != late {
			e.reserved = append(e.reserved, n)
		}
	}
	n1b := e.stakeVote(n1, prop, v1.OptionAbstain)
	e.reserved = []*wnote{snapErth}
	require.True(t, late.spent, "the vote's fee was paid from the post-snapshot note")
	require.Equal(t, n1.amount, n1b.amount)
	require.Equal(t, supplyB, e.app.ShieldedStakingKeeper.Supply(e.ctx(), e.valoper(vB)), "re-minted, not created")
	again, _, _, _ := e.stakeVoteMsg(n1b, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, true)
	res = e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrNoVoting.ABCICode(), res.Code, res.Log)
	replay, _, _, _ := e.stakeVoteMsg(n1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), false, false)
	res = e.checkTx(e.privateTx(replay))
	require.Equal(t, sstypes.ErrStakeNullifierSpent.ABCICode(), res.Code, res.Log)
	// The snapshot root stays good for a stake vote after it leaves the
	// stake tree's window; for anything else it is gone.
	{
		ctx := e.ctx()
		sp, err := e.app.ShieldedStakingKeeper.Params.Get(ctx)
		require.NoError(t, err)
		sp.StakeRootWindowSeconds = 1
		require.NoError(t, e.app.ShieldedStakingKeeper.Params.Set(ctx, sp))
	}
	e.next(5 * time.Second)
	oldFee := e.feeOnly()
	oldPlan := e.stake(&stakePlan{denom: n2.denom, ins: []*snote{n2}, outs: []*snote{e.freshStake(n2.denom, n2.amount-1)},
		vOut: 1, atSize: snap.TreeSize})
	oldPlan.proof.SpcCiphertext = shieldedtest.BlindCT("old")
	old := &sstypes.MsgUndelegate{Bundle: oldFee.b, Validator: e.valoper(vB), Amount: 1, Stake: oldPlan.proof}
	unproven(old)
	res = e.checkTx(e.privateTx(old))
	require.Equal(t, sstypes.ErrStakeTree.ABCICode(), res.Code, res.Log)
	// n3 Yes; position P Yes; n2 does not vote (vB inherits it).
	// One sighash binds the stake proof and the fee bundle: n3's vote with
	// another vote's (valid) fee bundle verifies no binding signature.
	sa, pa, spa, ba := e.stakeVoteMsg(n3, prop, v1.NewNonSplitVoteOption(v1.OptionYes), true, false)
	sb, _, _, _ := e.stakeVoteMsg(n2, prop, v1.NewNonSplitVoteOption(v1.OptionNo), true, false)
	spliced := *sa
	spliced.Bundle = sb.Bundle
	res = e.checkTx(e.privateTx(&spliced))
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), res.Code, res.Log)
	// A vote re-creates nothing itself: the chain re-mints its weight.
	making := *sa
	making.Stake.Commitments = [][]byte{privacy.FieldBytes(ssDet("made", 0)), sa.Stake.Commitments[1]}
	making.Stake.Ciphertexts = [][]byte{[]byte("made-ct"), sa.Stake.Ciphertexts[1]}
	res = e.checkTx(e.privateTx(&making))
	require.NotEqual(t, uint32(0), res.Code)
	require.Contains(t, res.Log, "creates no note")
	fb0 := e.run(e.privateTx(sa))
	require.Equal(t, uint32(0), fb0.Code, fb0.Log)
	e.settle(pa)
	e.settleStake(spa)
	e.mintedStake(fb0, ba)
	pv, pp, psp := e.positionVoteMsg(pid, pKey, prop, v1.OptionYes)
	e.prove(pv, pp)
	e.proveStake(pv, psp)
	fb := e.run(e.privateTx(pv))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pp)
	// Only the position's owner can vote it.
	pv2, _, _ := e.positionVoteMsg(pid, positionKey(9), prop, v1.OptionNo)
	unproven(pv2)
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
	// Each validator's deduction is capped at what its voted derth is worth
	// now (voted x rate_now, in its shares); the cap scales every option alike.
	capScale := func(v sdk.ValAddress, m math.LegacyDec, voted uint64) math.LegacyDec {
		ded := frac(m, voted, supply[e.valoper(v)])
		b, sNow, err := e.app.ShieldedStakingKeeper.Backing(cc, e.valoper(v))
		require.NoError(t, err)
		bonded, shares, _ := info(v)
		worth := math.LegacyNewDecFromInt(math.NewIntFromUint64(voted)).MulInt(b).QuoInt(sNow).Mul(shares).QuoInt(bonded)
		if ded.GT(worth) {
			return worth.Quo(ded)
		}
		return math.LegacyOneDec()
	}
	scaleA, scaleB := capScale(vA, mA, n3.amount), capScale(vB, mB, n1.amount+n4.amount)
	dedA := frac(mA, n3.amount, supply[e.valoper(vA)]).Mul(scaleA)
	dedB1 := frac(mB, n1.amount, supply[e.valoper(vB)]).Mul(scaleB)
	dedBP := frac(mB, n4.amount, supply[e.valoper(vB)]).Mul(scaleB)
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

	{ // Re-audit R4 (POC-B1): stake delegated to vB after the snapshot by
		// holders who did not vote (here 100k ERTH: module shares and derth
		// supply both grow, as an epoch end's delegation of new derth does)
		// follows vB's inherited vote, not the snapshot voters' fraction.
		cc2, _ := e.ctx().CacheContext()
		v, err := sk.GetValidator(cc2, vB)
		require.NoError(t, err)
		extra := math.NewInt(100_000 * ssErth)
		vs, err := e.app.ShieldedStakingKeeper.ValidatorState(cc2, e.valoper(vB))
		require.NoError(t, err)
		b0, s0, err := e.app.ShieldedStakingKeeper.Backing(cc2, e.valoper(vB))
		require.NoError(t, err)
		newDerth := extra.Mul(s0).Quo(b0) // minted at the current rate
		_, sh, err := sk.AddValidatorTokensAndShares(cc2, v, extra)
		require.NoError(t, err)
		d, err := sk.GetDelegation(cc2, mod, vB)
		require.NoError(t, err)
		d.Shares = d.Shares.Add(sh)
		require.NoError(t, sk.SetDelegation(cc2, d))
		coins := sdk.NewCoins(sdk.NewCoin("uerth", extra))
		require.NoError(t, e.app.BankKeeper.MintCoins(cc2, earthtypes.ModuleName, coins))
		require.NoError(t, e.app.BankKeeper.SendCoinsFromModuleToModule(cc2, earthtypes.ModuleName, stakingtypes.BondedPoolName, coins))
		vs.DerthSupply = vs.DerthSupply.Add(newDerth)
		require.NoError(t, e.app.ShieldedStakingKeeper.Validators.Set(cc2, e.valoper(vB), vs))
		p2, err := e.app.GovKeeper.Proposals.Get(cc2, prop)
		require.NoError(t, err)
		_, _, got2, err := e.app.GovKeeper.Tally(cc2, p2)
		require.NoError(t, err)
		abstain0, _ := math.NewIntFromString(got.AbstainCount)
		abstain1, _ := math.NewIntFromString(got2.AbstainCount)
		yes0, _ := math.NewIntFromString(got.YesCount)
		yes1, _ := math.NewIntFromString(got2.YesCount)
		t.Logf("POC-B1 abstain %s -> %s, yes %s -> %s", abstain0, abstain1, yes0, yes1)
		// n1's abstain is what n1's derth is worth: it barely moves (the new
		// stake was minted at the same rate). The new stake goes to vB's
		// own vote (Yes).
		require.True(t, abstain1.Sub(abstain0).Abs().LTE(abstain0.QuoRaw(1000).AddRaw(4)), "abstain %s -> %s", abstain0, abstain1)
		require.True(t, yes1.Sub(yes0).Sub(extra).Abs().LTE(extra.QuoRaw(1000)), "yes %s -> %s", yes0, yes1)
	}

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
	// All of vB's positions are one weighted voter: here just this one.
	vkey := sstypes.ValidatorVoterKey(vB)
	voter, err := ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), vkey))
	require.NoError(t, err)
	require.Equal(t, wantW, voter.Weight)
	require.Equal(t, []allocationtypes.OptionWeight{{OptionId: 1, Weight: wantW}}, voter.OptionWeights)
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
	voter, err = ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), vkey))
	require.NoError(t, err)
	require.Equal(t, p.Weight, voter.Weight)
	// The stream now carries weight, so its emission index runs.
	idx, err := ak.RewardIndex.Get(e.ctx(), uint32(gw))
	require.NoError(t, err)
	require.True(t, idx.IsPositive(), "Groundworks emission accrues to the position's split")

	// Update: clear the split (its owner proves the position's owner tag); a
	// replay of the tx is refused (its fee notes are spent); anyone else's
	// proof (another salt: another owner tag) is refused.
	up := func(splits []allocationtypes.AllocationWeight, salt fr.Element) (*sstypes.MsgUpdatePosition, *pendingBundle, *stakePlan) {
		pt := e.feeOnly()
		sp := e.ownerProof(salt, nil)
		return &sstypes.MsgUpdatePosition{Bundle: pt.b, PositionId: id, Splits: splits, Stake: sp.proof}, pt, sp
	}
	m, pt, sp := up(nil, key)
	e.prove(m, pt)
	e.proveStake(m, sp)
	fb := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	_, err = ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), vkey))
	require.ErrorIs(t, err, collections.ErrNotFound)
	res := e.checkTx(e.privateTx(m))
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), res.Code, res.Log)
	wrong, _, _ := up(opt, positionKey(9))
	unproven(wrong)
	res = e.checkTx(e.privateTx(wrong))
	require.Equal(t, sstypes.ErrSignature.ABCICode(), res.Code, res.Log)

	// Unlock: the derth comes back as a stake note of the same owner.
	pt = e.feeOnly()
	back := e.freshStake(dn.denom, 0)
	usp := e.ownerProof(key, back)
	um := &sstypes.MsgUnlockPosition{Bundle: pt.b, PositionId: id, Stake: usp.proof}
	e.prove(um, pt)
	e.proveStake(um, usp)
	fb = e.run(e.privateTx(um))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	back = e.mintedStake(fb, back)
	require.Equal(t, uint64(500*ssErth), back.amount)
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
	idA := e.lock(dnA, dnA.amount, positionKey(2), opt)
	posW := e.position(id).Weight
	require.True(t, posW.IsPositive())
	sumVoters := func() math.Int {
		return voterW(op).Add(voterW(sstypes.ValidatorVoterKey(vB))).Add(voterW(sstypes.ValidatorVoterKey(vA)))
	}

	// The operator votes with its self-bond (100 ERTH, plus the rewards the
	// epoch compounded into it).
	fb := e.run(e.signedTx(e.val, 300_000, 5_000, &allocationtypes.MsgSetAllocations{
		Creator: e.bech(op), Stream: gw, Percentages: opt,
	}))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	require.True(t, voterW(op).GT(math.NewInt(100*ssErth)))
	require.Equal(t, bonded(), voterW(op))

	// Bond more: the weight follows (AfterDelegationModified).
	w0 := voterW(op)
	fb = e.run(e.signedTx(e.val, 300_000, 5_000, stakingtypes.NewMsgDelegate(e.bech(op), e.valoper(vA), sdk.NewInt64Coin("uerth", 50*ssErth))))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	require.True(t, voterW(op).Sub(w0).Sub(math.NewInt(50*ssErth)).Abs().LTE(math.OneInt()))
	require.Equal(t, bonded(), voterW(op))

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
	require.Equal(t, posW, voterW(sstypes.ValidatorVoterKey(vB)))
	w1 := voterW(op)
	e.days(1) // an epoch: positions reweigh; the operator's self-bond compounds its rewards
	require.True(t, bonded().GT(w1), "self-bond compounded: %s", bonded())
	require.Equal(t, bonded(), voterW(op), "the weight follows the compounded bond")
	require.Equal(t, e.position(id).Weight, voterW(sstypes.ValidatorVoterKey(vB)))
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
	require.Equal(t, pA.Weight, voterW(sstypes.ValidatorVoterKey(vA)))
	live, err := e.app.ShieldedStakingKeeper.Rate(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	require.True(t, live.Sub(stA.EpochRate).Abs().LT(math.LegacyNewDecWithPrec(1, 6)), "epoch rate %s live %s", stA.EpochRate, live)
	require.Equal(t, posBBefore, e.position(id).Weight)
	require.Equal(t, posBBefore, voterW(sstypes.ValidatorVoterKey(vB)))
	o, err = ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
	require.NoError(t, err)
	require.Equal(t, sumVoters(), o.AmountAllocated)
	require.NoError(t, ak.AssertHotInvariants(e.ctx()))
	e.invariants()
}

// A validator operator's self-bond rewards and its commission (uerth) are
// re-delegated to its validator at every epoch end: the bond grows by them,
// the operator's Groundworks weight follows, no commission is left accrued.
// A jailed validator is skipped; a validator whose compounding fails is
// skipped and reported while the others compound and the block commits.
func TestSelfBondCompounds(t *testing.T) {
	e := initStakeEnv(t)
	vB, vBKey := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	opA, opB := sdk.AccAddress(e.genesisValidator()), sdk.AccAddress(vB)
	selfBond := func(op sdk.AccAddress, v sdk.ValAddress) math.Int {
		d, err := e.app.StakingKeeper.GetDelegation(e.ctx(), op, v)
		require.NoError(t, err)
		val, err := e.app.StakingKeeper.GetValidator(e.ctx(), v)
		require.NoError(t, err)
		return val.TokensFromShares(d.Shares).TruncateInt()
	}
	commission := func(v sdk.ValAddress) math.Int {
		c, err := e.app.DistrKeeper.GetValidatorAccumulatedCommission(e.ctx(), v)
		require.NoError(t, err)
		return c.Commission.AmountOf("uerth").TruncateInt()
	}

	// The operator votes on Groundworks with its self-bond, so the weight
	// resync is visible.
	gw := allocationtypes.STREAM_ID_GROUNDWORKS
	gov := authtypes.NewModuleAddress("gov")
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))))
	_, err := allocationkeeper.NewMsgServerImpl(e.app.AllocationKeeper).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: gw, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	fb := e.run(e.signedTx(vBKey, 300_000, 5_000, &allocationtypes.MsgSetAllocations{
		Creator: e.bech(opB), Stream: gw, Percentages: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}},
	}))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	weight := func() math.Int {
		v, err := e.app.AllocationKeeper.Voters.Get(e.ctx(), collections.Join(uint32(gw), []byte(opB)))
		require.NoError(t, err)
		return v.Weight
	}

	// --- an epoch: both self-bonds grow by their rewards and commission;
	// the event says by how much.
	e.next(time.Hour)
	a0, b0 := selfBond(opA, e.genesisValidator()), selfBond(opB, vB)
	balB := e.app.BankKeeper.GetBalance(e.ctx(), opB, "uerth").Amount
	var res *abci.ResponseFinalizeBlock
	for i := 0; i < 2 && res == nil; i++ {
		r := e.next(24 * time.Hour)
		if len(eventsOf(r.Events, sstypes.EventTypeEpoch)) > 0 {
			res = r
		}
	}
	require.NotNil(t, res, "no epoch end")
	compounded := map[string]math.Int{}
	for _, ev := range eventsOf(res.Events, sstypes.EventTypeSelfBond) {
		amt, ok := math.NewIntFromString(ev["amount"])
		require.True(t, ok)
		compounded[ev["validator"]] = amt
	}
	require.Len(t, compounded, 2)
	for _, c := range []struct {
		op  sdk.AccAddress
		v   sdk.ValAddress
		was math.Int
	}{{opA, e.genesisValidator(), a0}, {opB, vB, b0}} {
		got := selfBond(c.op, c.v)
		add := compounded[e.valoper(c.v)]
		require.True(t, add.IsPositive(), "%s", e.valoper(c.v))
		require.True(t, commission(c.v).IsZero(), "%s: commission compounded", e.valoper(c.v))
		require.True(t, got.Sub(c.was).Sub(add).Abs().LTE(math.OneInt()), "%s: %s -> %s, compounded %s", e.valoper(c.v), c.was, got, add)
	}
	require.Equal(t, balB, e.app.BankKeeper.GetBalance(e.ctx(), opB, "uerth").Amount, "the rewards were bonded, not paid out")
	bondedB, err := e.app.StakingKeeper.GetDelegatorBonded(e.ctx(), opB)
	require.NoError(t, err)
	require.Equal(t, bondedB, weight(), "Groundworks weight follows the compounded self-bond")
	require.NoError(t, e.app.AllocationKeeper.AssertHotInvariants(e.ctx()))
	e.invariants()

	// Commission accrues again, and compounds at the next epoch end: it
	// cannot be withdrawn (TestOperatorRewardClaimRefused).
	e.next(time.Hour)
	require.True(t, commission(vB).IsPositive())

	// --- halt safety: vB's compounding panics (its distribution starting
	// info claims more stake than it has). The epoch still ends, vA still
	// compounds, the failure is reported, the block commits. Repaired, vB
	// compounds again.
	vBAddr := vB
	info, err := e.app.DistrKeeper.GetDelegatorStartingInfo(e.ctx(), vBAddr, opB)
	require.NoError(t, err)
	bad := info
	bad.Stake = info.Stake.MulInt64(1000)
	require.NoError(t, e.app.DistrKeeper.SetDelegatorStartingInfo(e.ctx(), vBAddr, opB, bad))
	b1, a1 := selfBond(opB, vB), selfBond(opA, e.genesisValidator())
	res = e.next(24 * time.Hour)
	fails := eventsOf(res.Events, sstypes.EventTypeEpochFailure)
	require.Len(t, fails, 1)
	require.Equal(t, "self_bond", fails[0]["stage"])
	require.Equal(t, e.valoper(vB), fails[0]["validator"])
	require.Equal(t, b1, selfBond(opB, vB), "vB's work rolled back")
	require.True(t, selfBond(opA, e.genesisValidator()).GT(a1), "vA still compounded")
	require.NoError(t, e.app.DistrKeeper.SetDelegatorStartingInfo(e.ctx(), vBAddr, opB, info))
	res = e.next(24 * time.Hour)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure))
	require.True(t, selfBond(opB, vB).GT(b1), "repaired: vB compounds again")

	// --- jailed: skipped.
	ctx := e.ctx()
	val, err := e.app.StakingKeeper.GetValidator(ctx, vB)
	require.NoError(t, err)
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)
	require.NoError(t, e.app.StakingKeeper.Jail(ctx, consAddr))
	e.next(5 * time.Second)
	b2 := selfBond(opB, vB)
	res = e.next(24 * time.Hour)
	for _, ev := range eventsOf(res.Events, sstypes.EventTypeSelfBond) {
		require.NotEqual(t, e.valoper(vB), ev["validator"], "a jailed validator does not compound")
	}
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure))
	require.Equal(t, b2, selfBond(opB, vB))
	e.invariants()
}

// Stake notes are owner-locked and derth is nobody's coin: a restake whose
// output belongs to another owner has no witness (the circuit refuses it),
// and a proof made for one output cannot be passed off for another's; an
// honest restake splits within the owner; derth can be neither a pool asset,
// a dex pool token nor a swap's output.
func TestStakeNotesOwnerLocked(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(3_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	dn := e.delegate(vB, uint64(1_000*ssErth))
	v := e.valoper(vB)
	asset := privacy.AssetID(dn.denom)
	other := privacy.OwnerPK(ssDet("nk", 99))

	restake := func(outs ...*snote) (*sstypes.MsgRestake, *pendingBundle, *stakePlan) {
		p := e.feeOnly()
		sp := e.stake(&stakePlan{denom: dn.denom, ins: []*snote{dn}, outs: outs})
		return &sstypes.MsgRestake{Bundle: p.b, Validator: v, Stake: sp.proof}, p, sp
	}

	// The transfer attempt: an output note of another owner. No witness
	// exists (proven only when the circuits are at hand: a refused witness
	// leaves nothing to cache).
	a, b := e.freshStake(dn.denom, 400*uint64(ssErth)), e.freshStake(dn.denom, 600*uint64(ssErth))
	steal, _, ssp := restake(a, b)
	steal.Stake.Commitments[0] = privacy.FieldBytes(privacy.StakeCM(asset, a.amount, privacy.StakePC(other, a.rho, a.rcm)))
	ssp.proof = steal.Stake
	_, err := e.tryProveStake(steal, ssp)
	if shieldedtest.Circuits() != "" {
		require.ErrorIs(t, err, shieldedtest.ErrWitnessRefused)
	} else {
		require.ErrorIs(t, err, shieldedtest.ErrNoCircuits)
	}

	// An honest split, and the same proof with its first output swapped for
	// another owner's (fee bundle re-proven over the forged msg): the stake
	// proof no longer verifies.
	honest, hp, hsp := restake(a, b)
	e.prove(honest, hp)
	e.proveStake(honest, hsp)
	forged := *honest
	forged.Stake.Commitments = [][]byte{privacy.FieldBytes(privacy.StakeCM(asset, a.amount, privacy.StakePC(other, a.rho, a.rcm))),
		honest.Stake.Commitments[1]}
	fb := e.feeOnly()
	forged.Bundle = fb.b
	e.prove(&forged, fb)
	res := e.checkTx(e.privateTx(&forged))
	require.Equal(t, sstypes.ErrInvalidStakeProof.ABCICode(), res.Code, res.Log)
	r := e.run(e.privateTx(honest))
	require.Equal(t, uint32(0), r.Code, r.Log)
	e.settle(hp)
	e.settleStake(hsp)
	require.True(t, a.known && b.known)
	require.Equal(t, uint64(1_000*ssErth), e.stakeBalance(dn.denom), "split, still the owner's")
	e.invariants()

	// derth is never a pool asset, a dex token or a swap output.
	_, err = e.app.ShieldedKeeper.RegisterAsset(e.ctx(), dn.denom)
	require.Error(t, err)
	cp := e.run(e.signedTx(e.user, 600_000, 5_000, &dextypes.MsgCreatePool{Creator: e.bech(e.userAddr()),
		AmountA: sdk.NewInt64Coin("uerth", ssErth), AmountB: sdk.NewInt64Coin(dn.denom, 1)}))
	require.Equal(t, dextypes.ErrShieldedOnly.ABCICode(), cp.Code, cp.Log)
	in := e.w.unspent("uerth", uint64(ssErth))
	sw := e.build(spend{denom: "uerth", inputs: []*wnote{in}, valueOut: uint64(ssErth)})
	out := e.w.fresh(dn.denom, 0)
	swap := &dextypes.MsgNoteSwap{Bundle: sw.b, DenomIn: "uerth", AmountIn: uint64(ssErth), DenomOut: dn.denom, MinAmountOut: 1,
		Pc: privacy.FieldBytes(e.w.pc(out)), Ciphertext: shieldedtest.BlindCT("derth-swap")}
	unproven(swap)
	res = e.checkTx(e.privateTx(swap))
	require.Equal(t, dextypes.ErrPoolNotFound.ABCICode(), res.Code, res.Log)
}
