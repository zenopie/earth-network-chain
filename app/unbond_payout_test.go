package app

import (
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// fakeUndelegate drives MsgUndelegate's handler (ante faked) for amount
// derth of val, paying out to a pc derived from label.
func (e *stakeEnv) fakeUndelegate(val sdk.ValAddress, amount uint64, label string) *sstypes.MsgUndelegateResponse {
	e.t.Helper()
	pc := privacy.FieldBytes(ssDet("payout-pc/"+label, 0))
	m := &sstypes.MsgUndelegate{Validator: e.valoper(val), Amount: amount,
		Stake: fakeStake("undelegate/"+label, false), Pc: pc, Ciphertext: shieldedtest.BlindCT("payout/" + label)}
	res, err := sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper).Undelegate(e.fakeAuthorized(m), m)
	require.NoError(e.t, err)
	return res
}

// payoutEvents are the payout events (made, failed) for id so far.
func (e *stakeEnv) payoutEvents(typ string, id uint64) []map[string]string {
	var out []map[string]string
	for _, ev := range eventsOf(e.events, typ) {
		if ev[sstypes.AttributeKeyPayoutID] == strconv.FormatUint(id, 10) {
			out = append(out, ev)
		}
	}
	return out
}

// An undelegation pays out by itself: the chain mints value x payout /
// requested to the msg's pc, with the msg's ciphertext, one block after the
// record matures. Many payouts are spread over blocks (UnbondPayoutSweepLimit
// a block). A payout that fails is kept, retried later with backoff, and
// paid once it can be; it never blocks the payouts behind it. The books
// balance throughout.
func TestUnbondPayoutsSweepRetryNeverDrop(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	res := e.fakeDelegate(v, uint64(10_000*ssErth), "staker")
	e.next(25 * time.Hour)

	// 120 undelegations in one epoch: one record, 120 payouts.
	const n = 120
	ids := make([]uint64, n)
	var total uint64
	for i := range ids {
		r := e.fakeUndelegate(v, res.Derth/(2*n), strconv.Itoa(i))
		ids[i] = r.PayoutId
		total += r.Value
		require.Equal(t, uint64(i), r.PayoutId, "ids are sequential")
	}
	e.invariants()
	ep := e.record(v, 2)
	require.Equal(t, math.NewIntFromUint64(total), ep.Requested)
	require.Equal(t, ep.Requested, ep.Outstanding)

	// Governance disables uerth sends: the pool refuses module mints of it,
	// so the first payout attempts must fail and be kept.
	setSend := func(on bool) { e.app.BankKeeper.SetSendEnabled(e.ctx(), "uerth", on) }
	e.next(25 * time.Hour) // the epoch end: one SDK undelegation
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(v, 2).Status)
	e.invariants()

	// Run to maturity with sends disabled for the block after it.
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), e.app.ShieldedStakingKeeper.ModuleAddress(), v)
	require.NoError(t, err)
	matures := ubd.Entries[0].CompletionTime
	for !e.now.Add(25 * time.Hour).After(matures) {
		e.next(25 * time.Hour)
	}
	e.next(matures.Sub(e.now) + time.Second) // matures in this block; paid from the next
	r := e.record(v, 2)
	require.Equal(t, sstypes.UNBOND_STATUS_MATURED, r.Status)
	require.Empty(t, eventsOf(e.events, sstypes.EventTypeUnbondPayout), "nothing paid in the maturity block")
	e.invariants()

	setSend(false)
	e.next(5 * time.Second)
	// The first 50 tried and failed: each kept, attempts 1, retried in an hour.
	fails := eventsOf(e.events, sstypes.EventTypeUnbondPayoutFailed)
	require.Len(t, fails, sstypes.UnbondPayoutSweepLimit)
	for _, id := range ids[:sstypes.UnbondPayoutSweepLimit] {
		p, err := e.app.ShieldedStakingKeeper.UnbondPayouts.Get(e.ctx(), id)
		require.NoError(t, err, "a failed payout is kept")
		require.EqualValues(t, 1, p.PayoutAttempts)
		require.Equal(t, e.now.Unix()+3600, p.RetryAt)
	}
	e.invariants()

	// Sends back: the untried payouts go first (the failed ones wait their
	// hour), 50 a block.
	setSend(true)
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	paid := eventsOf(e.events, sstypes.EventTypeUnbondPayout)
	require.Len(t, paid, n-sstypes.UnbondPayoutSweepLimit)
	e.invariants()
	// Nothing more until the retries are due.
	e.next(5 * time.Second)
	require.Len(t, eventsOf(e.events, sstypes.EventTypeUnbondPayout), n-sstypes.UnbondPayoutSweepLimit)
	e.next(time.Hour)
	require.Len(t, eventsOf(e.events, sstypes.EventTypeUnbondPayout), n)

	// Every payout: value x payout / requested, minted as one note with the
	// msg's ciphertext; the record is gone, its dust in the community pool.
	sum := math.ZeroInt()
	for i, id := range ids {
		evs := e.payoutEvents(sstypes.EventTypeUnbondPayout, id)
		require.Len(t, evs, 1)
		value, _ := math.NewIntFromString(evs[0][sstypes.AttributeKeyValue])
		amount, _ := math.NewIntFromString(evs[0][sstypes.AttributeKeyAmount])
		require.Equal(t, value.Mul(r.Payout).Quo(r.Requested), amount)
		require.Equal(t, "1", evs[0][sstypes.AttributeKeyNotes])
		sum = sum.Add(amount)
		_, err := e.app.ShieldedStakingKeeper.UnbondPayouts.Get(e.ctx(), id)
		require.ErrorIs(t, err, collections.ErrNotFound)
		if i < sstypes.UnbondPayoutSweepLimit {
			require.Len(t, e.payoutEvents(sstypes.EventTypeUnbondPayoutFailed, id), 1)
		}
	}
	require.True(t, sum.LTE(r.Payout) && r.Payout.Sub(sum).LT(math.NewInt(n)), "dust only")
	mints := 0
	for _, ev := range eventsOf(e.events, shieldedtypes.EventTypeMint) {
		if ev[shieldedtypes.AttributeKeyModule] == sstypes.ModuleName {
			mints++
		}
	}
	require.Equal(t, n, mints)
	_, err = e.app.ShieldedStakingKeeper.UnbondRecords.Get(e.ctx(), collections.Join(e.valoper(v), uint64(2)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	has, err := e.app.ShieldedStakingKeeper.MaturedRecords.Has(e.ctx(), collections.Join(e.valoper(v), uint64(2)))
	require.NoError(t, err)
	require.False(t, has)
	e.invariants()
}

// A payout queued across an export is re-imported and paid; genesis refuses
// a payout that does not add up to its record.
func TestUnbondPayoutGenesisRoundTrip(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(10_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	res := e.fakeDelegate(v, uint64(1_000*ssErth), "g")
	e.next(25 * time.Hour)
	u := e.fakeUndelegate(v, res.Derth/4, "g")
	gs, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	require.Len(t, gs.UnbondPayouts, 1)
	require.Equal(t, u.PayoutId, gs.UnbondPayouts[0].Id)
	require.Equal(t, u.PayoutId+1, gs.NextUnbondPayoutId)
	require.NoError(t, gs.Validate())

	bad := *gs
	bad.UnbondPayouts = nil
	require.ErrorContains(t, bad.Validate(), "payouts sum to 0")
	bad = *gs
	bad.NextUnbondPayoutId = 0
	require.ErrorContains(t, bad.Validate(), "next_unbond_payout_id")
	bad = *gs
	p := gs.UnbondPayouts[0]
	p.Ciphertext = p.Ciphertext[1:]
	bad.UnbondPayouts = []sstypes.UnbondPayout{p}
	require.Error(t, bad.Validate())
	bad = *gs
	p = gs.UnbondPayouts[0]
	p.PayoutAttempts = 1
	bad.UnbondPayouts = []sstypes.UnbondPayout{p}
	require.ErrorContains(t, bad.Validate(), "retry_at")
}
