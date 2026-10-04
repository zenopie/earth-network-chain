package app

import (
	"encoding/json"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
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

// Audit 7, A7-2: a zero-height export with open moves did not re-import:
// x/staking's prep moves every redelegation entry to height 0 while the
// moves kept their entry heights. The open moves are now dropped (no slash on
// the new chain reaches a height-0 entry); their debt rows stay, so a label
// still clears at its row's value, or whole when its move was never
// slashed. Two entries of one pair at height 0 are accepted, and every
// invariant holds after the prep and after the import.
func TestAuditA7ZeroHeightExportWithMoves(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vA, uint64(4_000*ssErth), "a")
	e.auditDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	k := e.app.ShieldedStakingKeeper

	// A slashed move (a debt row) and, in a later block, one never slashed.
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	_, err := e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "slashed")
	require.NoError(t, err)
	e.doubleSign(vA, infraction)
	slashedKey, cleanKey := fakeMoveKey("slashed"), fakeMoveKey("clean")
	row, err := k.DebtRetained.Get(e.ctx(), slashedKey)
	require.NoError(t, err, "the slashed move's debt row")
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(500*ssErth), "clean")
	require.NoError(t, err)
	e.next(5 * time.Second)
	_, ok := e.move(cleanKey)
	require.True(t, ok)
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 2)
	e.invariants()

	// The prep alone keeps every invariant (10 included).
	ctx, _ := e.ctx().CacheContext()
	e.app.prepForZeroHeightGenesis(ctx, nil)
	require.NoError(t, k.AssertInvariants(ctx))
	n := 0
	require.NoError(t, k.Moves.Walk(ctx, nil, func([]byte, sstypes.Move) (bool, error) { n++; return false, nil }))
	require.Zero(t, n, "the open moves are dropped")

	// The export re-imports, with both entries at height 0, no moves, and
	// the debt rows kept.
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
	fred, err := fresh.StakingKeeper.GetRedelegation(fctx, k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, fred.Entries, 2)
	for _, en := range fred.Entries {
		require.Zero(t, en.CreationHeight)
	}
	gs, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Empty(t, gs.Moves)
	got, err := fresh.ShieldedStakingKeeper.DebtRetained.Get(fctx, slashedKey)
	require.NoError(t, err, "the slashed label still clears at its row")
	require.Equal(t, row, got)
	_, err = fresh.ShieldedStakingKeeper.DebtRetained.Get(fctx, cleanKey)
	require.Error(t, err, "the clean label clears whole: no row")
	r0, _, err := k.DebtRoot(e.ctx())
	require.NoError(t, err)
	r1, _, err := fresh.ShieldedStakingKeeper.DebtRoot(fctx)
	require.NoError(t, err)
	require.Equal(t, r0, r1, "the same debt tree")
}

// Audit 7, A7-L2: which entries a slash reached was inferred from the count
// of x/staking's unbonds (the last that many), but x/staking makes none for
// an entry whose slash truncates to nothing, which governance makes common
// by lowering a slash fraction (0.0001: every entry under 10,000 uerth). The
// slash is now replayed with x/slashing's fractions: the large entry it
// reached owes it, the dust entry after it, which it skipped, owes nothing.
func TestAuditA7SlashSkipsDustEntry(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vA, uint64(4_000*ssErth), "a")
	e.auditDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	sp, err := e.app.SlashingKeeper.GetParams(ctx)
	require.NoError(t, err)
	sp.SlashFractionDoubleSign = math.LegacyNewDecWithPrec(1, 4)
	require.NoError(t, e.app.SlashingKeeper.SetParams(ctx, sp))

	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "large")
	require.NoError(t, err)
	e.next(5 * time.Second)
	// A dust entry after it (a move whose bonded part is 5,000 uerth),
	// filled in directly.
	ctx = e.ctx()
	red, err := e.app.StakingKeeper.GetRedelegation(ctx, k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	dust := red.Entries[0]
	dust.CreationHeight = ctx.BlockHeight()
	dust.InitialBalance = math.NewInt(5_000)
	dust.SharesDst = math.LegacyNewDec(5_000)
	dust.UnbondingId += 1_000
	red.Entries = append(red.Entries, dust)
	require.NoError(t, e.app.StakingKeeper.SetRedelegation(ctx, red))
	dustKey := privacy.FieldBytes(ssDet("dust-move", 0))
	mv := sstypes.Move{Key: dustKey, SrcValidator: e.valoper(vA), DstValidator: e.valoper(vB), Height: dust.CreationHeight,
		MoveTime: uint64(e.now.Unix()), Credited: math.NewInt(5_000), Shares: dust.SharesDst, EntryHeight: dust.CreationHeight,
		Completion: dust.CompletionTime.UnixNano(), Retained: math.NewInt(5_000)}
	require.NoError(t, k.Moves.Set(ctx, dustKey, mv))
	require.NoError(t, k.MovesByEntry.Set(ctx, collections.Join3(e.valoper(vA)+"/"+e.valoper(vB), mv.EntryHeight, dustKey)))
	require.NoError(t, k.MovesByCompletion.Set(ctx, collections.Join(mv.Completion, dustKey)))
	e.next(5 * time.Second)
	e.invariants()

	res := e.doubleSign(vA, infraction)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "the slash is attributed")
	sd := eventsOf(res.Events, sstypes.EventTypeSlashDebt)
	require.Len(t, sd, 1)
	require.Equal(t, "1", sd[0]["entries"], "x/staking unbonded once: the large entry")
	_, err = k.DebtRetained.Get(e.ctx(), fakeMoveKey("large"))
	require.NoError(t, err, "the large move owes the slash")
	_, err = k.DebtRetained.Get(e.ctx(), dustKey)
	require.Error(t, err, "the dust move, which the slash skipped, owes nothing")
	e.invariants()
}

// Audit 7, B L-1 / A7-L3: a stake proof whose clear_before was non-zero only
// when it cleared a label marked the tx as clearing one, which the public
// redelegations into that validator could link to its owner. Now
// every stake proof must name the label window's current clear_before (the
// block time less the window, within ClearBeforeSlackSeconds) and the
// current debt root, whether it clears anything or not.
func TestStakeNotesNameClearBefore(t *testing.T) {
	e := initStakeEnv(t)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(1_000 * ssErth))
	e.shield(uint64(1_000 * ssErth))
	k := e.app.ShieldedStakingKeeper
	cb, err := k.ClearBefore(e.ctx())
	require.NotZero(t, cb)
	in := e.w.unspent("uerth", uint64(200*ssErth))
	require.NotNil(t, in)
	m, p, sp := e.delegateMsg(v, in, uint64(200*ssErth))
	require.Equal(t, cb, m.Stake.ClearBefore, "a proof that clears nothing names it too")
	root, _, err := k.DebtRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, root, m.Stake.DebtRoot)
	zero := make([]byte, 32)
	for _, bad := range []struct {
		cb   uint64
		root []byte
		why  string
	}{
		{0, zero, "clear_before"}, // the old encoding of "clears nothing"
		{cb + 1_000, root, "clear_before"},
		{cb - sstypes.ClearBeforeSlackSeconds - 1, root, "clear_before"},
		{cb, privacy.FieldBytes(ssDet("stale-debt-root", 0)), "debt root"},
	} {
		mb := *m
		mb.Stake.ClearBefore, mb.Stake.DebtRoot = bad.cb, bad.root
		res := e.run(e.privateTx(&mb))
		require.NotEqual(t, uint32(0), res.Code, "clear_before %d", bad.cb)
		require.Contains(t, res.Log, bad.why)
	}
	res := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	e.invariants()
}
