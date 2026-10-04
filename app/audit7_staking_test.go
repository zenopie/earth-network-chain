package app

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// exactRedelegate drives a redelegation (ante faked, as fakeRedelegate)
// crediting all that the value buys at dst but for x/staking's truncation of
// the bonded part (a uerth, plus one for the rounding of the buy): the
// mover's credit is as close to its value as the chain accepts, so what it
// keeps is what the move is worth.
func (e *stakeEnv) exactRedelegate(src, dst sdk.ValAddress, amount uint64, label string) *sstypes.MsgRedelegateResponse {
	e.t.Helper()
	k := e.app.ShieldedStakingKeeper
	bA, sA, err := k.Backing(e.ctx(), e.valoper(src))
	require.NoError(e.t, err)
	u := math.NewIntFromUint64(amount).Mul(bA).Quo(sA)
	bB, sB, err := k.Backing(e.ctx(), e.valoper(dst))
	require.NoError(e.t, err)
	credit := u.SubRaw(2).Mul(sB).Quo(bB)
	m := &sstypes.MsgRedelegate{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		DstDerth: credit.Uint64(), MoveTime: uint64(e.now.Unix()), Stake: fakeStake("redelegate/"+label, true)}
	res, err := sskeeper.NewActionHandler(k).ExecutePrivateAction(e.ctx(), m, nil)
	require.NoError(e.t, err)
	return res.(*sstypes.MsgRedelegateResponse)
}

// a7Outcome is one run of the A7-1 scenario: what A's remaining holders lost
// to the slash (a fraction of their value) and what the attacker's stake is
// worth after it (uerth).
type a7Outcome struct {
	holdersLoss math.LegacyDec
	attacker    math.LegacyDec
	queued      math.Int
	bonded      math.Int
}

// runA7Scenario: A has a 1,000 ERTH self-bond; an attacker holds 1,000 ERTH
// of derth/A and an honest holder 3,000, both bonded; a newcomer queues
// 2,000 in the current epoch. A double-signs; then, if move, the attacker
// moves all its derth/A to B (it saw the evidence coming); then the
// evidence lands.
func runA7Scenario(t *testing.T, move bool) a7Outcome {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	att := e.auditDelegate(vA, uint64(1_000*ssErth), "attacker")
	e.auditDelegate(vA, uint64(3_000*ssErth), "honest")
	e.auditDelegate(vB, uint64(3_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	e.auditDelegate(vA, uint64(2_000*ssErth), "newcomer") // queued this epoch
	require.True(t, e.state(vA).PendingDelegation.GTE(math.NewInt(2_000*ssErth)))

	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	out := a7Outcome{queued: math.ZeroInt(), bonded: math.ZeroInt()}
	var key []byte
	if move {
		r := e.exactRedelegate(vA, vB, att.Derth, "attacker")
		key = fakeMoveKey("attacker")
		out.queued = math.NewIntFromUint64(r.Value) // refined from the move below
		mv, ok := e.move(key)
		require.True(t, ok, "the bonded part's move: the slash reaches it")
		require.Greater(t, mv.EntryHeight, infraction)
	}
	before := e.rate(vA)
	e.doubleSign(vA, infraction)
	after := e.rate(vA)
	out.holdersLoss = math.LegacyOneDec().Sub(after.Quo(before))
	if move {
		mv, ok := e.move(key)
		require.True(t, ok)
		require.True(t, mv.Retained.LT(mv.Credited), "the mover's label owes the slash: %s of %s", mv.Retained, mv.Credited)
		out.attacker = e.rate(vB).MulInt(mv.Retained)
	} else {
		out.attacker = after.MulInt64(int64(att.Derth))
	}
	e.invariants()
	return out
}

// Audit 7, A7-1: a redelegation took its value out of the source's queue
// first. That part carried no entry, so a mover who saw a slash of the source
// coming left with the unslashable slice of the book, and its remaining
// holders paid the mover's share (PoC: the attacker avoided 33.3 ERTH of a 5%
// slash; A's holders lost 4.00% instead of 3.33%). A move now leaves the
// queue and the bonded stake pro rata: the mover pays its share through its
// label, and the remaining holders lose no more than without the move.
func TestAuditA7QueueEscapesSlash(t *testing.T) {
	stay := runA7Scenario(t, false)
	moved := runA7Scenario(t, true)
	t.Logf("no move: holders lose %s, attacker worth %s", stay.holdersLoss, stay.attacker)
	t.Logf("move:    holders lose %s, attacker worth %s", moved.holdersLoss, moved.attacker)
	require.True(t, stay.holdersLoss.IsPositive())
	// The remaining holders lose no more than without the move (a part in
	// 10^4 for the rounding and the block's rewards).
	tol := math.LegacyNewDecWithPrec(1, 4)
	require.True(t, moved.holdersLoss.LTE(stay.holdersLoss.Mul(math.LegacyOneDec().Add(tol))),
		"holders lose %s with the move, %s without", moved.holdersLoss, stay.holdersLoss)
	// The mover pays its share: worth no more than had it stayed.
	require.True(t, moved.attacker.LTE(stay.attacker.Mul(math.LegacyOneDec().Add(tol))),
		"the mover is worth %s, %s had it stayed", moved.attacker, stay.attacker)
	require.True(t, moved.attacker.GTE(stay.attacker.Mul(math.LegacyOneDec().Sub(math.LegacyNewDecWithPrec(1, 3)))),
		"nor much less: %s against %s", moved.attacker, stay.attacker)
}

// The queue-first path is kept only where no slash can reach the source's
// stake: a validator the module holds no bonded stake at yet (its first
// delegations wait in its queue) moves the value out of the queue alone, with
// no entry and no move, since a slash of it takes nothing from the book.
func TestAuditA7QueueOnlyWithoutBondedStake(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	d := e.auditDelegate(vA, uint64(2_000*ssErth), "first") // queued, nothing bonded at A
	require.True(t, e.modDelegation(vA).IsZero())
	r, err := e.fakeRedelegate(vA, vB, d.Derth/2, "queued")
	require.NoError(t, err)
	require.Zero(t, r.CompletionTime)
	_, ok := e.move(fakeMoveKey("queued"))
	require.False(t, ok)
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), e.app.ShieldedStakingKeeper.ModuleAddress(), vA, vB)
	require.Error(t, err, "no x/staking entry")
	e.invariants()
}
