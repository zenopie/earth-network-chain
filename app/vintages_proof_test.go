package app

// Stake vintages with real proofs (fixtures: scripts/staking-fixtures.sh):
// a check-in, and a retired note's withdrawal on its unsuffixed (base
// vintage) asset, through the private ante.

import (
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

func sskeeperHandler(e *stakeEnv) sskeeper.ActionHandler {
	return sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper)
}

// A note of January's vintage checks in in February: lane A spends it (the
// change stays in January), the credit lane merges the amount into a new,
// unlabelled February note. Supply moves between the vintages; nothing else.
func TestCheckInProof(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	jan := e.month()
	dn := e.delegate(vB, uint64(2_000*ssErth))
	require.Equal(t, sstypes.DerthDenom(e.valoper(vB)), dn.denom, "January is the base vintage: the unsuffixed denom")
	e.days(1)
	feb := sstypes.AddMonths(jan, 1)
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	vc, err := e.app.ShieldedStakingKeeper.VintageContext(e.ctx())
	require.NoError(t, err)
	require.Equal(t, feb, vc.Current)
	febDenom := sstypes.VintageDenom(e.valoper(vB), feb, vc.Base)
	b0, s0, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(vB))
	require.NoError(t, err)

	amount := uint64(1_500 * ssErth)
	p := e.feeOnly()
	sp := e.stake(&stakePlan{denom: dn.denom, ins: []*snote{dn}, vOut: amount, out: e.freshStake(dn.denom, dn.amount-amount),
		credit: &creditLane{denom: febDenom, vIn: amount}})
	require.False(t, sp.credit.out.labelled(), "a check-in's credit carries no label")
	m := &sstypes.MsgCheckIn{Bundle: p.b, Validator: e.valoper(vB), FromVintage: jan, Amount: amount, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	gas, err := sskeeperHandler(e).PrivateActionGas(e.ctx(), m)
	require.NoError(t, err)
	restake := &sstypes.MsgRestake{Validator: e.valoper(vB), Stake: sp.proof}
	rgas, err := sskeeperHandler(e).PrivateActionGas(e.ctx(), restake)
	require.NoError(t, err)
	require.Greater(t, gas, rgas, "a restake's base, over two lanes")

	res := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	require.True(t, sp.credit.out.known)
	require.Equal(t, febDenom, sp.credit.out.denom)
	require.Equal(t, math.NewIntFromUint64(dn.amount-amount), e.vsupply(vB, jan))
	require.Equal(t, math.NewIntFromUint64(amount), e.vsupply(vB, feb))
	b1, s1, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, s0, s1)
	require.True(t, b1.GTE(b0), "no value moved (rewards only)")
	ev := eventsOf(res.Events, sstypes.EventTypeCheckIn)
	require.Len(t, ev, 1)
	require.Equal(t, strconv.FormatUint(amount, 10), ev[0]["derth"])
	e.invariants()

	// The same proof again: its nullifiers are spent.
	again := e.run(e.privateTx(m))
	require.NotEqual(t, uint32(0), again.Code)
}

// A note whose vintage retired withdraws its share of the held value: the
// whole note in lane A (v_out = its amount, a zero note out, no vote), on
// the unsuffixed asset a pre-v1.3.0 wallet built it with. Paid at the
// record's maturity, at the record's payout rate.
func TestRetiredWithdrawalProof(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	jan := e.month()
	dn := e.delegate(vB, uint64(2_000*ssErth))
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	e.fakeDelegate(vB, uint64(1_000*ssErth), "live") // another holder, in February

	e.toTime(sstypes.VintageRetiresAt(jan).Add(time.Minute))
	rv, ok := e.retiredVintage(vB, jan)
	require.True(t, ok)
	require.Equal(t, math.NewIntFromUint64(dn.amount), rv.Supply)

	// A restake of the retired note is refused in the ante, before its proof.
	{
		p := e.feeOnly()
		sp := e.stake(&stakePlan{denom: dn.denom, ins: []*snote{dn}, out: e.freshStake(dn.denom, dn.amount)})
		m := &sstypes.MsgRestake{Bundle: p.b, Validator: e.valoper(vB), Stake: sp.proof}
		unproven(m)
		res := e.run(e.privateTx(m))
		require.NotEqual(t, uint32(0), res.Code)
		require.Contains(t, res.Log, "retired")
	}

	m, p, sp, out := e.undelegateMsg(vB, dn, dn.amount)
	require.Nil(t, sp.out, "the whole note: a zero note out")
	res := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	ev := eventsOf(res.Events, sstypes.EventTypeRetiredWithdrawal)
	require.Len(t, ev, 1)
	require.Equal(t, rv.Value.String(), ev[0]["value"], "the whole note: all the held value")
	id, err := strconv.ParseUint(ev[0]["payout_id"], 10, 64)
	require.NoError(t, err)
	_, ok = e.retiredVintage(vB, jan)
	require.False(t, ok, "withdrawn whole")
	e.invariants()

	e.days(1)
	e.days(22)
	un := &unbonding{id: id, epoch: rv.Epoch, value: rv.Value.Uint64(), out: out}
	got := e.payout(un)
	require.NotNil(t, got)
	requested, payout := e.matured(vB, rv.Epoch)
	require.Equal(t, rv.Value.Mul(payout).Quo(requested).Uint64(), got.value)
	e.invariants()
}
