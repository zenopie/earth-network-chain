package app

import (
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Stake vintages, audit 1 fix pass: one test per finding.

// gwTotalsRaw is the Groundworks totals store with undecoded values.
func gwTotalsRaw(app *App) collections.Map[collections.Triple[string, uint32, uint64], []byte] {
	sb := collections.NewSchemaBuilder(runtimeKV(app))
	return collections.NewMap(sb, sstypes.GwVintageTotalsKey, "gw_totals_raw",
		collections.TripleKeyCodec(collections.StringKey, collections.Uint32Key, collections.Uint64Key), collections.BytesValue)
}

// L1. A book that fails the monthly pass in its first block is retried a
// day later even though the pass's last block succeeded: 260 books, two
// blocks, the failure in the first.
func TestVintageSweepRetryAcrossBlocks(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	jan := e.month()
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.next(5 * time.Second)

	// 259 more books (state only: the pass reads nothing else).
	ctx := e.ctx()
	books := []string{e.valoper(vA)}
	for i := 0; i < 259; i++ {
		s, err := e.app.StakingKeeper.ValidatorAddressCodec().BytesToString(sdk.ValAddress(privacy.FieldBytes(ssDet(fmt.Sprintf("book/%d", i), 0))[:20]))
		require.NoError(t, err)
		vs, err := k.ValidatorState(ctx, s)
		require.NoError(t, err)
		require.NoError(t, k.Validators.Set(ctx, s, vs))
		books = append(books, s)
	}
	sort.Strings(books)
	require.Greater(t, len(books), sstypes.VintageRetireLimit)
	// The first book in key order fails: an undecodable total of a vintage
	// retired by the next month's pass.
	bad := books[0]
	stale := sstypes.AddMonths(jan, -13)
	raw := gwTotalsRaw(e.app)
	key := collections.Join3(bad, stale, uint64(1))
	require.NoError(t, raw.Set(ctx, key, []byte{0xff, 0x00, 0xfe}))

	// The pass of the next month: chunk one fails the book, chunk two ends
	// the pass without a failure of its own.
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Minute))
	sweep, err := k.VintageSweep.Get(e.ctx())
	require.NoError(t, err)
	require.True(t, sweep.Active, "%d books: two blocks", len(books))
	require.True(t, sweep.Failed, "the first block's failure is carried")
	require.NotEmpty(t, eventsOf(e.events, sstypes.EventTypeEpochFailure))
	e.next(5 * time.Second)
	sweep, err = k.VintageSweep.Get(e.ctx())
	require.NoError(t, err)
	require.False(t, sweep.Active)
	require.False(t, sweep.Failed)
	require.NotZero(t, sweep.RetryAt, "the pass failed a book: it runs again in a day (audit 1, L1)")
	require.Equal(t, e.now.Unix()+sstypes.VintageRetryDelay, sweep.RetryAt)

	// Repaired: the retry a day later drops the book's stale total.
	require.NoError(t, raw.Remove(e.ctx(), key))
	require.NoError(t, k.GwTotals.Set(e.ctx(), key, math.NewInt(7)))
	e.next(23 * time.Hour)
	has, err := k.GwTotals.Has(e.ctx(), key)
	require.NoError(t, err)
	require.True(t, has, "not before the retry time")
	e.next(time.Hour)
	has, err = k.GwTotals.Has(e.ctx(), key)
	require.NoError(t, err)
	require.False(t, has, "retried and dropped")
	sweep, err = k.VintageSweep.Get(e.ctx())
	require.NoError(t, err)
	require.False(t, sweep.Active)
	require.Zero(t, sweep.RetryAt, "a clean pass")
}

// L1/L3. A vintage whose booking fails at an epoch end stays in its book,
// earning for its holders, and is booked at the next epoch end: a day, not
// a month.
func TestVintageBookingRetriesNextEpoch(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	jan := e.month()
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	e.fakeDelegate(vA, uint64(1_000*ssErth), "feb")
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-time.Hour)) // an epoch end
	// A stray RetiredVintage for January makes its booking fail (L6's guard).
	stray := sstypes.RetiredVintage{Validator: e.valoper(vA), Vintage: jan, Supply: math.OneInt(), Value: math.ZeroInt(),
		RemainingSupply: math.OneInt(), RemainingValue: math.ZeroInt()}
	require.NoError(t, k.RetiredVintages.Set(e.ctx(), collections.Join(e.valoper(vA), jan), stray))
	ep, err := k.Epoch.Get(e.ctx())
	require.NoError(t, err)
	e.toTime(time.Unix(ep.EndTime, 0))
	require.Equal(t, math.NewIntFromUint64(e.exactDerthSupply(vA, jan)), e.vsupply(vA, jan), "not booked: still in the book")
	failures := eventsOf(e.events, sstypes.EventTypeEpochFailure)
	require.NotEmpty(t, failures)
	require.Equal(t, "retire_vintage", failures[0]["stage"])
	require.NoError(t, k.RetiredVintages.Remove(e.ctx(), collections.Join(e.valoper(vA), jan)))
	e.days(1)
	rv, ok := e.retiredVintage(vA, jan)
	require.True(t, ok, "booked at the next epoch end")
	require.True(t, rv.Value.IsPositive())
	e.requireVintageBooks()
}

// exactDerthSupply is S_{val,m} as a uint64.
func (e *stakeEnv) exactDerthSupply(val sdk.ValAddress, m uint32) uint64 {
	return e.vsupply(val, m).Uint64()
}

// extendEntry moves the completion of the module's (src, dst) redelegation
// entry holding move key to c, as a chain of merges (mergeOldEntries) can:
// the entry and its moves stay slashable to c, past the label window.
func (e *stakeEnv) extendEntry(src, dst sdk.ValAddress, key []byte, c time.Time) {
	e.t.Helper()
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	mv, ok := e.move(key)
	require.True(e.t, ok)
	red, err := e.app.StakingKeeper.GetRedelegation(ctx, k.ModuleAddress(), src, dst)
	require.NoError(e.t, err)
	found := false
	for i := range red.Entries {
		if red.Entries[i].CreationHeight == mv.EntryHeight && red.Entries[i].CompletionTime.UnixNano() == mv.Completion {
			red.Entries[i].CompletionTime = c
			found = true
		}
	}
	require.True(e.t, found)
	require.NoError(e.t, e.app.StakingKeeper.SetRedelegation(ctx, red))
	require.NoError(e.t, e.app.StakingKeeper.InsertRedelegationQueue(ctx, red, c))
	require.NoError(e.t, k.MovesByCompletion.Remove(ctx, collections.Join(mv.Completion, mv.Key)))
	mv.Completion = c.UnixNano()
	require.NoError(e.t, k.Moves.Set(ctx, mv.Key, mv))
	require.NoError(e.t, k.MovesByCompletion.Set(ctx, collections.Join(mv.Completion, mv.Key)))
}

// lateSlash runs L2's scenario at unbonding time ut: a move from A to B
// whose x/staking entry merges kept slashable for about 2 x ut is slashed
// (an infraction of A before it) at `at` (given the move's time and its
// credit vintage), after its label window closed. Returns B, the credit
// vintage, B's backing and supply just before the slash, and the slash's
// slash_debt event.
type lateSlashRun struct {
	e        *stakeEnv
	vB       sdk.ValAddress
	m        uint32
	b0, s0   math.Int
	vsBefore math.Int
	ev       map[string]string
}

func lateSlash(t *testing.T, ut time.Duration, later bool, at func(t0 time.Time, m uint32) time.Time) lateSlashRun {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	params, err := e.app.StakingKeeper.GetParams(e.ctx())
	require.NoError(t, err)
	params.UnbondingTime = ut
	require.NoError(t, e.app.StakingKeeper.SetParams(e.ctx(), params))
	e.fakeDelegate(vA, uint64(20_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(5_000*ssErth), "b") // B's other holders, in the same vintage
	e.days(1)
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	m := e.month()
	_, err = e.tryFakeRedelegateFrom(vA, vB, 0, uint64(4_000*ssErth), "late")
	require.NoError(t, err)
	mv, ok := e.move(fakeMoveKey("late"))
	require.True(t, ok)
	require.Equal(t, m, mv.Vintage)
	t0 := time.Unix(int64(mv.MoveTime), 0)
	e.next(5 * time.Second)
	e.extendEntry(vA, vB, mv.Key, t0.Add(2*ut-time.Hour))
	e.next(5 * time.Second)
	if later {
		// Live holders at B in a later vintage, so B keeps a delegation
		// once the move's vintage retires.
		e.toTime(sstypes.VintageStart(sstypes.AddMonths(m, 1)).Add(time.Hour))
		e.fakeDelegate(vB, uint64(5_000*ssErth), "b-later")
	}

	e.toTime(at(t0, m))
	e.next(5 * time.Second)
	cb, err := k.ClearBefore(e.ctx())
	require.NoError(t, err)
	require.Greater(t, cb, mv.MoveTime, "the label window has closed: the note may have cleared")
	_, ok = e.move(mv.Key)
	require.True(t, ok, "still slashable: its merged entry has not matured")
	r := lateSlashRun{e: e, vB: vB, m: m, vsBefore: e.vsupply(vB, m)}
	r.b0, r.s0, err = k.Backing(e.ctx(), e.valoper(vB))
	require.NoError(t, err)
	res := e.doubleSign(vA, infraction)
	evs := eventsOf(res.Events, sstypes.EventTypeSlashDebt)
	require.Len(t, evs, 1)
	r.ev = evs[0]
	return r
}

// requireAbsorbed: the late move owes nothing on its note and no vintage
// is charged; B's supply is unchanged and its rate fell (its live holders
// bear the burn, once).
func (r lateSlashRun) requireAbsorbed(t *testing.T) {
	require.Equal(t, "1", r.ev["late_moves"])
	require.Equal(t, "0", r.ev["debt"])
	mv, ok := r.e.move(fakeMoveKey("late"))
	require.True(t, ok)
	require.Equal(t, mv.Credited, mv.Retained, "no debt row: the note may hold its whole exposure")
	has, err := r.e.app.ShieldedStakingKeeper.DebtRetained.Has(r.e.ctx(), mv.Key)
	require.NoError(t, err)
	require.False(t, has, "no debt row")
	b, s, err := r.e.app.ShieldedStakingKeeper.Backing(r.e.ctx(), r.e.valoper(r.vB))
	require.NoError(t, err)
	require.Equal(t, r.s0, s, "no supply comes off: no note shrinks")
	require.True(t, b.LT(r.b0), "B's rate fell")
}

// L2, case A (21-day unbonding, a live vintage): the late slash charges no
// note and no vintage; every holder of the credit's vintage can leave.
func TestVintageLateSlashDebtLive(t *testing.T) {
	ut := 21 * 24 * time.Hour
	r := lateSlash(t, ut, false, func(t0 time.Time, _ uint32) time.Time { return t0.Add(ut + 2*time.Hour) })
	r.requireAbsorbed(t)
	require.Equal(t, r.vsBefore, r.e.vsupply(r.vB, r.m), "the credit's vintage keeps its supply")
	_, err := r.e.tryFakeUndelegate(r.vB, r.m, r.e.vsupply(r.vB, r.m).Uint64(), "all")
	require.NoError(t, err, "its whole supply undelegates: no holder is left short")
	r.e.next(5 * time.Second)
	r.e.requireVintageBooks()
}

// L2, case B (a 300-day unbonding, above the ~182.5 days that let a merged
// entry outlast its vintage): the slash lands after the credit's vintage
// retired. Nothing is charged to it: its notes withdraw all of its held
// value (nothing stranded) while B's rate bears the burn once.
func TestVintageLateSlashDebtRetired(t *testing.T) {
	ut := 300 * 24 * time.Hour
	r := lateSlash(t, ut, true, func(_ time.Time, m uint32) time.Time { return sstypes.VintageRetiresAt(m).Add(2 * time.Hour) })
	rv, ok := r.e.retiredVintage(r.vB, r.m)
	require.True(t, ok, "the credit's vintage retired before the slash")
	r.requireAbsorbed(t)
	rv2, ok := r.e.retiredVintage(r.vB, r.m)
	require.True(t, ok)
	require.Equal(t, rv, rv2, "the retired vintage is untouched")
	res, err := r.e.tryFakeUndelegate(r.vB, r.m, rv.RemainingSupply.Uint64(), "all")
	require.NoError(t, err)
	require.Equal(t, rv.RemainingValue.Uint64(), res.Value, "all of the held value is withdrawn: nothing stranded")
	r.e.next(5 * time.Second)
	r.e.requireVintageBooks()
	// The highest clear_before named survives an export.
	k := r.e.app.ShieldedStakingKeeper
	require.NoError(t, k.MaxClearBefore.Set(r.e.ctx(), 12345))
	gs, err := k.ExportGenesis(r.e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(12345), gs.MaxClearBefore)
}

// L3. The value a retiring vintage earns between its boundary and the epoch
// end goes to its own holders: valued at the epoch end, in the step that
// undelegates it. vA's book retires whole, but a sniper delegates right
// after the boundary: the sniper's derth grows no faster than the retired
// holders' value. vC retires whole with nobody joining: all of its book
// goes to the retired holders.
func TestVintageRetiredTailRewards(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	jan := e.month()
	e.fakeDelegate(vA, uint64(50_000*ssErth), "jan-a")
	e.fakeDelegate(vC, uint64(50_000*ssErth), "jan-c")
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-time.Hour)) // an epoch end
	e.toTime(ret.Add(time.Minute))
	ep, err := k.Epoch.Get(e.ctx())
	require.NoError(t, err)
	require.Greater(t, ep.EndTime, e.now.Unix()+3600)

	rate := func(v sdk.ValAddress) math.LegacyDec {
		r, err := k.Rate(e.ctx(), e.valoper(v))
		require.NoError(t, err)
		return r
	}
	sA, sC := e.vsupply(vA, jan), e.vsupply(vC, jan)
	r0A, r0C := rate(vA), rate(vC)
	// The sniper: the smallest delegation, into the wholly retiring book.
	amt := uint64(10 * ssErth)
	sn := e.fakeDelegate(vA, amt, "sniper")
	for i := 0; i < 30; i++ {
		e.next(time.Minute) // rewards accrue on the bonded value
	}
	require.True(t, rate(vA).GT(r0A), "rewards since the boundary")
	e.toTime(time.Unix(ep.EndTime, 0))

	rvA, ok := e.retiredVintage(vA, jan)
	require.True(t, ok)
	rvC, ok := e.retiredVintage(vC, jan)
	require.True(t, ok)
	// The retired value includes the tail: more than the boundary's rate.
	require.True(t, rvA.Value.GT(r0A.MulInt(sA).TruncateInt()), "vA: %s at %s", rvA.Value, r0A)
	require.True(t, rvC.Value.GT(r0C.MulInt(sC).TruncateInt()), "vC: %s at %s", rvC.Value, r0C)
	// The sniper's value grew at most as the retired holders' did.
	b, s, err := k.Backing(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(sn.Derth), s, "only the sniper is live at vA")
	retiredGrowth := math.LegacyNewDecFromInt(rvA.Value).QuoInt(sA).Quo(r0A)
	sniperGrowth := math.LegacyNewDecFromInt(b).Quo(math.LegacyNewDec(int64(amt)))
	require.True(t, sniperGrowth.LTE(retiredGrowth.Add(math.LegacyNewDecWithPrec(1, 6))),
		"sniper %s, retired holders %s", sniperGrowth, retiredGrowth)
	// vC: nothing left in the book; its record took the whole delegation.
	require.True(t, e.state(vC).DerthSupply.IsZero())
	e.requireVintageBooks()
}

// L4. The dead-vote sweep never stalls: more than a block's worth of
// orphaned index entries and of votes that cannot be read are dropped or
// parked (with an event), and a real dead vote behind them is deleted.
func TestVintageDeadVoteSweepNoStall(t *testing.T) {
	g := initGwEnv(t)
	k := g.app.ShieldedStakingKeeper
	jan := g.month()
	real := g.castGw(g.v, 1_000*gwE, g.split(100))
	g.next(5 * time.Second)
	// Sorting before the real vote: a lower validator string, the same
	// (retired) vintage.
	ctx := g.ctx()
	sb := collections.NewSchemaBuilder(runtimeKV(g.app))
	rawVotes := collections.NewMap(sb, sstypes.GwVotesKey, "gw_votes_raw", collections.Uint64Key, collections.BytesValue)
	n := sstypes.DeadVoteSweepLimit + 5
	for i := 0; i < n; i++ {
		id := uint64(1_000_000 + i)
		require.NoError(t, k.GwVotesByVintage.Set(ctx, collections.Join3(jan, "a-orphan", id)))
		if i%2 == 0 {
			require.NoError(t, rawVotes.Set(ctx, id, []byte{0xff, 0xff, 0xff})) // unreadable
		}
	}
	g.toTime(sstypes.VintageRetiresAt(jan).Add(time.Minute))
	for i := 0; i < 3 && g.hasVote(real.id); i++ {
		g.next(5 * time.Second)
	}
	require.False(t, g.hasVote(real.id), "the real dead vote is swept behind them")
	left := 0
	require.NoError(t, k.GwVotesByVintage.Walk(g.ctx(), nil, func(key collections.Triple[uint32, string, uint64]) (bool, error) {
		if key.K1() == jan {
			left++
		}
		return false, nil
	}))
	require.Zero(t, left, "every entry dropped or parked")
	// Parked votes stay (counting for nothing) until cleared by hand here.
	for i := 0; i < n; i += 2 {
		require.NoError(t, rawVotes.Remove(g.ctx(), uint64(1_000_000+i)))
	}
	g.requireVintageBooks()
}

// L5. The base vintage never falls back to the current month, and the
// upgrade fails (halting at its height) rather than run without its
// vintage accounting: here, an unbonding_time above the cap.
func TestVintageBaseStrictAndUpgradeFailsClosed(t *testing.T) {
	g := initGwEnv(t)
	k := g.app.ShieldedStakingKeeper
	ctx, _ := g.ctx().CacheContext()
	require.NoError(t, k.BaseVintage.Remove(ctx))
	_, err := k.VintageContext(ctx)
	require.ErrorIs(t, err, sstypes.ErrInvariant, "unset: an error, not the current month")

	ctx, _ = g.ctx().CacheContext()
	require.NoError(t, k.BaseVintage.Remove(ctx))
	require.NoError(t, k.VintageSupplies.Clear(ctx, nil))
	params, err := g.app.StakingKeeper.GetParams(ctx)
	require.NoError(t, err)
	params.UnbondingTime = (sstypes.MaxUnbondingSeconds + 1) * time.Second
	require.NoError(t, g.app.StakingKeeper.SetParams(ctx, params))
	_, err = v1_3_0Handler(g.app)(ctx, upgradetypes.Plan{Name: UpgradeV1_3_0}, g.app.ModuleManager.GetVersionMap())
	require.ErrorIs(t, err, sstypes.ErrInvariant)
	has, err := k.BaseVintage.Has(ctx)
	require.NoError(t, err)
	require.False(t, has, "nothing written")
	n := 0
	require.NoError(t, k.VintageSupplies.Walk(ctx, nil, func(collections.Pair[string, uint32], math.Int) (bool, error) { n++; return false, nil }))
	require.Zero(t, n)
}

// L6. A vintage both supplied and retired is refused at genesis.
func TestVintageGenesisSuppliedAndRetired(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.next(5 * time.Second)
	gs, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.NotEmpty(t, gs.VintageSupplies)
	vs := gs.VintageSupplies[0]
	gs.RetiredVintages = append(gs.RetiredVintages, sstypes.RetiredVintage{Validator: vs.Validator, Vintage: vs.Vintage,
		Supply: math.OneInt(), Value: math.ZeroInt(), RemainingSupply: math.OneInt(), RemainingValue: math.ZeroInt()})
	require.ErrorContains(t, gs.Validate(), "both")
}

// Month-boundary grace: a credit proven just before 00:00 UTC on the 1st
// and included after it still lands, in the previous month's vintage,
// during the month's first VintageGraceSeconds; after them it is refused,
// in the ante as in the handler.
func TestVintageBoundaryGrace(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	e.fakeDelegate(vA, uint64(10_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b")
	feb, mar := sstypes.AddMonths(jan, 1), sstypes.AddMonths(jan, 2)
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	e.fakeDelegate(vA, uint64(1_000*ssErth), "feb")
	e.toTime(sstypes.VintageStart(mar).Add(10 * time.Minute))
	require.Equal(t, mar, e.month())

	// A delegation naming February.
	dm := fakeDelegateMsg(e.valoper(vA), uint64(ssErth), "late-delegate")
	dm.Derth, dm.Vintage = e.exactDerth(vA, dm.Amount), feb
	febA := e.vsupply(vA, feb)
	_, err := e.stakeSrv().Delegate(e.fakeAuthorized(dm), dm)
	require.NoError(t, err)
	require.Equal(t, febA.Add(math.NewIntFromUint64(dm.Derth)), e.vsupply(vA, feb), "lands in February")
	// A redelegation whose credit lane proved B's February asset.
	rm := &sstypes.MsgRedelegate{SrcValidator: e.valoper(vA), DstValidator: e.valoper(vB), Amount: uint64(100 * ssErth), Vintage: jan,
		CreditVintage: feb, DstDerth: e.quoteRedelegate(vA, vB, uint64(100*ssErth)), MoveTime: uint64(e.now.Unix()),
		Stake: fakeStake("redelegate/grace", true)}
	lanes, err := e.app.ShieldedStakingKeeper.StakeLanes(e.ctx(), rm)
	require.NoError(t, err)
	require.Equal(t, sstypes.VintageDenom(e.valoper(vB), feb, e.baseVintage()), lanes.CreditDenom)
	res, err := sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).ExecutePrivateAction(e.ctx(), rm, nil)
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(res.(*sstypes.MsgRedelegateResponse).Derth), e.vsupply(vB, feb))
	mv, ok := e.move(rm.MoveKey())
	require.True(t, ok)
	require.Equal(t, feb, mv.Vintage)
	// A check-in from January into February.
	ci := e.fakeCheckInMsg(vA, jan, uint64(ssErth), "grace")
	ci.CreditVintage = feb
	cr, err := e.tryFakeCheckIn(ci)
	require.NoError(t, err)
	require.Equal(t, feb, cr.Vintage)
	// Not into January (two months back), nor from February into itself.
	bad := e.fakeCheckInMsg(vA, jan, 1, "jan")
	bad.CreditVintage = jan
	_, err = e.tryFakeCheckIn(bad)
	require.ErrorIs(t, err, sstypes.ErrVintage)
	self := e.fakeCheckInMsg(vA, feb, 1, "self")
	self.CreditVintage = feb
	_, err = e.tryFakeCheckIn(self)
	require.ErrorIs(t, err, sstypes.ErrVintage)
	// A credit_vintage needs an explicit vintage (no two sighashes alike).
	rm2 := *rm
	rm2.Vintage = 0
	require.ErrorIs(t, rm2.ValidateBasic(), sstypes.ErrVintage)
	e.next(5 * time.Second)
	e.requireVintageBooks()

	// Past the grace: refused, in the handler and in the ante.
	e.toTime(sstypes.VintageStart(mar).Add(sstypes.VintageGraceSeconds*time.Second + time.Minute))
	dm2 := fakeDelegateMsg(e.valoper(vA), uint64(ssErth), "too-late")
	dm2.Derth, dm2.Vintage = e.exactDerth(vA, dm2.Amount), feb
	_, err = e.stakeSrv().Delegate(e.fakeAuthorized(dm2), dm2)
	require.ErrorIs(t, err, sstypes.ErrVintage)
	late := e.fakeCheckInMsg(vA, jan, 1, "too-late")
	late.CreditVintage = feb
	_, err = e.tryFakeCheckIn(late)
	require.ErrorIs(t, err, sstypes.ErrVintage)
	e.shield(uint64(100 * ssErth))
	p := e.feeOnly()
	am := &sstypes.MsgCheckIn{Bundle: p.b, Validator: e.valoper(vA), FromVintage: jan, Amount: 1, CreditVintage: feb,
		Stake: fakeStake("ante-late", true)}
	unproven(am)
	ares := e.run(e.privateTx(am))
	require.NotEqual(t, uint32(0), ares.Code)
	require.Contains(t, ares.Log, "a credit lands in the current vintage")
}

// baseVintage is the chain's base vintage.
func (e *stakeEnv) baseVintage() uint32 {
	vc, err := e.app.ShieldedStakingKeeper.VintageContext(e.ctx())
	require.NoError(e.t, err)
	return vc.Base
}

// Query/VintageBooks lists every validator's vintages, live, retiring and
// retired, without naming one; a walk of one-entry pages gives the same
// list; Query/VintageInfo is the clock.
func TestVintageBooksQuery(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	q := sskeeper.NewQueryServerImpl(k)
	jan := e.month()
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a-jan")
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a-feb")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b-feb")
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 2)).Add(time.Hour))
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b-mar")
	e.toTime(sstypes.VintageRetiresAt(jan).Add(-time.Hour)) // an epoch end
	e.toTime(sstypes.VintageRetiresAt(jan).Add(time.Minute))

	info, err := q.VintageInfo(e.ctx(), &sstypes.QueryVintageInfoRequest{})
	require.NoError(t, err)
	require.Equal(t, e.month(), info.CurrentVintage)
	require.Equal(t, e.baseVintage(), info.BaseVintage)

	all, err := q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{})
	require.NoError(t, err)
	require.Nil(t, all.Pagination.NextKey)
	state := map[string]string{}
	for _, en := range all.Entries {
		state[en.Validator+"/"+strconv.Itoa(int(en.Vintage))] = en.State
	}
	feb := sstypes.AddMonths(jan, 1)
	require.Equal(t, map[string]string{
		e.valoper(vA) + "/" + strconv.Itoa(int(jan)):                       "retiring",
		e.valoper(vA) + "/" + strconv.Itoa(int(feb)):                       "live",
		e.valoper(vB) + "/" + strconv.Itoa(int(feb)):                       "live",
		e.valoper(vB) + "/" + strconv.Itoa(int(sstypes.AddMonths(jan, 2))): "live",
	}, state)
	ep, err := k.Epoch.Get(e.ctx())
	require.NoError(t, err)
	e.toTime(time.Unix(ep.EndTime, 0))
	all, err = q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{})
	require.NoError(t, err)
	require.Len(t, all.Entries, 4)
	var retired *sstypes.VintageBookEntry
	for i := range all.Entries {
		if all.Entries[i].State == "retired" {
			retired = &all.Entries[i]
		}
	}
	require.NotNil(t, retired)
	require.Equal(t, jan, retired.Vintage)
	require.NotNil(t, retired.Retired)
	require.NotNil(t, retired.Record)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, retired.Record.Status)
	require.Equal(t, retired.Record.CompletionTime/int64(time.Second), retired.ExpectedCompletion)
	for i := 1; i < len(all.Entries); i++ {
		a, b := all.Entries[i-1], all.Entries[i]
		require.True(t, a.Validator < b.Validator || (a.Validator == b.Validator && a.Vintage < b.Vintage), "key order")
	}
	// One entry a page.
	var paged []sstypes.VintageBookEntry
	var next []byte
	for {
		res, err := q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{Pagination: &query.PageRequest{Key: next, Limit: 1}})
		require.NoError(t, err)
		require.LessOrEqual(t, len(res.Entries), 1)
		paged = append(paged, res.Entries...)
		if res.Pagination.NextKey == nil {
			break
		}
		next = res.Pagination.NextKey
	}
	require.Equal(t, all.Entries, paged)
	_, err = q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{Pagination: &query.PageRequest{Offset: 1}})
	require.Error(t, err)
	// reverse and count_total are refused too, not ignored (audit 2, I4).
	_, err = q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{Pagination: &query.PageRequest{Reverse: true}})
	require.ErrorContains(t, err, "reverse is not supported")
	_, err = q.VintageBooks(e.ctx(), &sstypes.QueryVintageBooksRequest{Pagination: &query.PageRequest{CountTotal: true}})
	require.ErrorContains(t, err, "count_total is not supported")
}
