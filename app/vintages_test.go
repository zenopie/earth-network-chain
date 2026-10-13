package app

// Stake vintages (v1.3.0, ORCHARD_DESIGN.md 8.9), driven through the stake
// handlers with the ante faked (fakeAuthorized: no proofs), except where a
// test says it proves.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/runtime"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// ---- helpers ------------------------------------------------------------------

// month is the vintage of the next block (its time is the last block's or
// later).
func (e *stakeEnv) month() uint32 { return sstypes.MonthOf(e.now) }

// toTime runs one block at t.
func (e *stakeEnv) toTime(t time.Time) { e.atUnix(t.Unix()) }

// vsupply is S_{val,m}.
func (e *stakeEnv) vsupply(val sdk.ValAddress, m uint32) math.Int {
	e.t.Helper()
	s, err := e.app.ShieldedStakingKeeper.VintageSupply(e.ctx(), e.valoper(val), m)
	require.NoError(e.t, err)
	return s
}

func (e *stakeEnv) retiredVintage(val sdk.ValAddress, m uint32) (sstypes.RetiredVintage, bool) {
	rv, err := e.app.ShieldedStakingKeeper.RetiredVintages.Get(e.ctx(), collections.Join(e.valoper(val), m))
	return rv, err == nil
}

func (e *stakeEnv) stakeSrv() sstypes.MsgServer {
	return sskeeper.NewMsgServerImpl(e.app.ShieldedStakingKeeper)
}

// tryFakeUndelegate drives MsgUndelegate's handler for amount derth of
// val's vintage (0: the base), paying to a pc of label.
func (e *stakeEnv) tryFakeUndelegate(val sdk.ValAddress, vintage uint32, amount uint64, label string) (*sstypes.MsgUndelegateResponse, error) {
	m := &sstypes.MsgUndelegate{Validator: e.valoper(val), Amount: amount, Vintage: vintage,
		Stake: fakeStake("undelegate/"+label, false), Pc: privacy.FieldBytes(ssDet("payout-pc/"+label, 0)),
		Ciphertext: shieldedtest.BlindCT("payout/" + label)}
	return e.stakeSrv().Undelegate(e.fakeAuthorized(m), m)
}

func (e *stakeEnv) fakeCheckInMsg(val sdk.ValAddress, from uint32, amount uint64, label string) *sstypes.MsgCheckIn {
	return &sstypes.MsgCheckIn{Validator: e.valoper(val), FromVintage: from, Amount: amount,
		Stake: fakeStake("checkin/"+label, true)}
}

func (e *stakeEnv) tryFakeCheckIn(m *sstypes.MsgCheckIn) (*sstypes.MsgCheckInResponse, error) {
	res, _, err := e.fakeCheckInEvents(m)
	return res, err
}

// fakeCheckInEvents is tryFakeCheckIn with the handler's events.
func (e *stakeEnv) fakeCheckInEvents(m *sstypes.MsgCheckIn) (*sstypes.MsgCheckInResponse, []map[string]string, error) {
	ctx := e.fakeAuthorized(m)
	res, err := e.stakeSrv().CheckIn(ctx, m)
	return res, eventsOf(ctx.EventManager().ABCIEvents(), sstypes.EventTypeCheckIn), err
}

func (e *stakeEnv) tryFakeRedelegateFrom(src, dst sdk.ValAddress, vintage uint32, amount uint64, label string) (*sstypes.MsgRedelegateResponse, error) {
	m := &sstypes.MsgRedelegate{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount, Vintage: vintage,
		DstDerth: e.quoteRedelegate(src, dst, amount), MoveTime: uint64(e.now.Unix()), Stake: fakeStake("redelegate/"+label, true)}
	res, err := sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).ExecutePrivateAction(e.ctx(), m, nil)
	if err != nil {
		return nil, err
	}
	return res.(*sstypes.MsgRedelegateResponse), nil
}

// requireVintageBooks: every book's supply is its vintages' sum, and every
// invariant holds.
func (e *stakeEnv) requireVintageBooks() {
	e.t.Helper()
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	sums := map[string]math.Int{}
	require.NoError(e.t, k.VintageSupplies.Walk(ctx, nil, func(key collections.Pair[string, uint32], s math.Int) (bool, error) {
		// Live, or retiring (past its boundary, booked at the next epoch
		// end): never one that has not begun, nor one booked as retired.
		cur := k.CurrentVintage(ctx)
		require.True(e.t, sstypes.VintageLive(key.K2(), cur) || sstypes.VintageRetired(key.K2(), cur), "vintage %d at %s has not begun", key.K2(), key.K1())
		has, err := k.RetiredVintages.Has(ctx, key)
		require.NoError(e.t, err)
		require.False(e.t, has, "vintage %d at %s is stored and retired", key.K2(), key.K1())
		if c, ok := sums[key.K1()]; ok {
			s = s.Add(c)
		}
		sums[key.K1()] = s
		return false, nil
	}))
	require.NoError(e.t, k.Validators.Walk(ctx, nil, func(v string, vs sstypes.ValidatorState) (bool, error) {
		got, ok := sums[v]
		if !ok {
			got = math.ZeroInt()
		}
		require.Equal(e.t, vs.DerthSupply.String(), got.String(), "%s: derth_supply is the sum of its stored vintages", v)
		return false, nil
	}))
	e.invariants()
}

// exportImport exports the chain and imports it into a fresh app at the
// next block's height and time.
func (e *stakeEnv) exportImport() (*App, sdk.Context) {
	e.t.Helper()
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(e.t, err)
	var appState map[string]json.RawMessage
	require.NoError(e.t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: e.t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(e.t, err)
	return fresh, fctx
}

// runtimeKV is x/shieldedstaking's store, for writing what an older
// binary left there.
func runtimeKV(app *App) corestore.KVStoreService {
	return runtime.NewKVStoreService(app.GetKey(sstypes.StoreKey))
}

// ---- supply per vintage ---------------------------------------------------------

// Every msg books its derth to the vintage it names: a delegation, a
// redelegation's credit and a check-in's to the current month; an
// undelegation, a redelegation's source and a check-in's source to the
// msg's. derth_supply is always the vintages' sum.
func TestVintageSupplyPerMsg(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	base, err := k.BaseVintage.Get(e.ctx())
	require.NoError(t, err)
	require.Equal(t, sstypes.MonthOf(ssGenesisTime), base, "a new chain's base vintage is its genesis month")
	jan := base

	d1 := e.fakeDelegate(vA, uint64(10_000*ssErth), "jan-a").Derth
	require.Equal(t, math.NewIntFromUint64(d1), e.vsupply(vA, jan))
	e.next(25 * time.Hour)
	e.requireVintageBooks()

	_, err = e.tryFakeUndelegate(vA, 0, uint64(1_000*ssErth), "jan-u")
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(d1-uint64(1_000*ssErth)), e.vsupply(vA, jan))
	r, err := e.tryFakeRedelegateFrom(vA, vB, 0, uint64(2_000*ssErth), "jan-r")
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(d1-uint64(3_000*ssErth)), e.vsupply(vA, jan))
	require.Equal(t, math.NewIntFromUint64(r.Derth), e.vsupply(vB, jan))
	mv, ok := e.move(fakeMoveKey("jan-r"))
	require.True(t, ok)
	require.Equal(t, jan, mv.Vintage, "a move records the vintage its credit landed in")
	e.requireVintageBooks()

	// February.
	feb := sstypes.AddMonths(jan, 1)
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	require.Equal(t, feb, e.month())
	d2 := e.fakeDelegate(vA, uint64(500*ssErth), "feb-a").Derth
	require.Equal(t, math.NewIntFromUint64(d2), e.vsupply(vA, feb))
	// A redelegation from January's vintage lands in dst's February one.
	r2, err := e.tryFakeRedelegateFrom(vA, vB, jan, uint64(1_000*ssErth), "feb-r")
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(r2.Derth), e.vsupply(vB, feb))
	require.Equal(t, math.NewIntFromUint64(d1-uint64(4_000*ssErth)), e.vsupply(vA, jan))
	// A check-in moves derth from January to February: no value moves.
	b0, s0, err := k.Backing(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	res, err := e.tryFakeCheckIn(e.fakeCheckInMsg(vA, jan, uint64(3_000*ssErth), "feb-c"))
	require.NoError(t, err)
	require.Equal(t, feb, res.Vintage)
	b1, s1, err := k.Backing(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	require.Equal(t, b0, b1, "a check-in moves no value")
	require.Equal(t, s0, s1, "nor supply")
	require.Equal(t, math.NewIntFromUint64(d1-uint64(7_000*ssErth)), e.vsupply(vA, jan))
	require.Equal(t, math.NewIntFromUint64(d2+uint64(3_000*ssErth)), e.vsupply(vA, feb))
	// An undelegation within February's vintage.
	_, err = e.tryFakeUndelegate(vA, feb, uint64(100*ssErth), "feb-u")
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(d2+uint64(2_900*ssErth)), e.vsupply(vA, feb))
	// More than a vintage holds is refused, whatever the book holds.
	_, err = e.tryFakeUndelegate(vA, jan, e.vsupply(vA, jan).Uint64()+1, "too-much")
	require.ErrorIs(t, err, sstypes.ErrAmount)
	_, err = e.tryFakeRedelegateFrom(vA, vB, jan, e.vsupply(vA, jan).Uint64()+1, "too-much")
	require.ErrorIs(t, err, sstypes.ErrAmount)
	e.next(5 * time.Second)
	e.requireVintageBooks()

	q, err := sskeeper.NewQueryServerImpl(k).Vintages(e.ctx(), &sstypes.QueryVintagesRequest{Validator: e.valoper(vA)})
	require.NoError(t, err)
	require.Equal(t, feb, q.CurrentVintage)
	require.Equal(t, jan, q.BaseVintage)
	require.Len(t, q.Live, 2)
	require.Equal(t, sstypes.DerthDenom(e.valoper(vA)), q.Live[0].Denom, "the base vintage's denom has no suffix")
	require.Equal(t, sstypes.DerthDenom(e.valoper(vA))+"/"+strconv.Itoa(int(feb)), q.Live[1].Denom)
	require.Equal(t, sstypes.VintageRetiresAt(jan).Unix(), q.Live[0].RetiresAt)
	require.Empty(t, q.Retired)
}

// A delegation credits only the current vintage; every msg refuses a
// vintage that is not one, or has not begun.
func TestVintageDelegateCurrentOnly(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	// Past the month's first VintageGraceSeconds (TestVintageBoundaryGrace).
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(sstypes.VintageGraceSeconds*time.Second + time.Minute))

	m := fakeDelegateMsg(e.valoper(vA), uint64(ssErth), "old")
	m.Derth = e.exactDerth(vA, m.Amount)
	_, err := e.stakeSrv().Delegate(e.fakeAuthorized(m), m)
	require.ErrorIs(t, err, sstypes.ErrVintage, "vintage 0 is January's: not the current one")
	m.Vintage = e.month()
	_, err = e.stakeSrv().Delegate(e.fakeAuthorized(m), m)
	require.NoError(t, err)

	r := &sstypes.MsgRestake{Validator: e.valoper(vA), Stake: fakeStake("future", false), Vintage: sstypes.AddMonths(e.month(), 1)}
	_, err = e.stakeSrv().Restake(e.fakeAuthorized(r), r)
	require.ErrorIs(t, err, sstypes.ErrVintage, "a vintage that has not begun")
	_, err = e.tryFakeUndelegate(vA, sstypes.AddMonths(e.month(), 2), 1, "future")
	require.ErrorIs(t, err, sstypes.ErrVintage)
	e.next(5 * time.Second)
	e.requireVintageBooks()
}

// ---- check-in -------------------------------------------------------------------

// A check-in moves a live, older vintage into the current one: refused from
// the current vintage, a future or a retired one, or for more than the
// vintage holds.
func TestVintageCheckIn(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	d := e.fakeDelegate(vA, uint64(5_000*ssErth), "a").Derth
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 2)).Add(time.Minute))
	mar := e.month()

	for name, c := range map[string]struct {
		from   uint32
		amount uint64
		err    error
	}{
		"current":      {mar, 1, sstypes.ErrVintage},
		"future":       {sstypes.AddMonths(mar, 1), 1, sstypes.ErrVintage},
		"retired":      {sstypes.AddMonths(mar, -13), 1, sstypes.ErrRetiredVintage},
		"too much":     {jan, d + 1, sstypes.ErrAmount},
		"empty":        {sstypes.AddMonths(jan, 1), 1, sstypes.ErrAmount},
		"not a month":  {202513 + 0*jan, 1, sstypes.ErrVintage},
		"before chain": {sstypes.AddMonths(jan, -1), 1, sstypes.ErrAmount},
	} {
		_, err := e.tryFakeCheckIn(e.fakeCheckInMsg(vA, c.from, c.amount, name))
		require.ErrorIs(t, err, c.err, name)
	}
	res, ev, err := e.fakeCheckInEvents(e.fakeCheckInMsg(vA, jan, d, "all"))
	require.NoError(t, err)
	require.Equal(t, mar, res.Vintage)
	require.True(t, e.vsupply(vA, jan).IsZero())
	require.Equal(t, math.NewIntFromUint64(d), e.vsupply(vA, mar))
	e.next(5 * time.Second)
	require.Len(t, ev, 1)
	require.Equal(t, strconv.Itoa(int(jan)), ev[0]["from_vintage"])
	require.Equal(t, strconv.Itoa(int(mar)), ev[0]["to_vintage"])
	require.Equal(t, strconv.FormatUint(d, 10), ev[0]["derth"])
	e.requireVintageBooks()

	// Through the ante (no proof needed: refused before it is verified).
	e.shield(uint64(100 * ssErth))
	p := e.feeOnly()
	m := &sstypes.MsgCheckIn{Bundle: p.b, Validator: e.valoper(vA), FromVintage: e.month(), Amount: 1,
		Stake: fakeStake("ante", true)}
	unproven(m)
	res2 := e.run(e.privateTx(m))
	require.NotEqual(t, uint32(0), res2.Code)
	require.Contains(t, res2.Log, "not older than")
}

// ---- retirement and withdrawal --------------------------------------------------

// A vintage retires at 00:00 UTC on the 1st of its month + 13: its supply
// leaves the book at the live rate, floored toward the pool, as one held
// undelegation; its notes withdraw their shares of it (before and after
// maturity, partly and whole); every other msg on it is refused.
func TestVintageRetirementAndWithdrawal(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	jan := e.month()
	feb := sstypes.AddMonths(jan, 1)
	e.fakeDelegate(vA, uint64(10_000*ssErth), "jan")
	e.days(3) // rewards: a rate above 1, so the value is floored
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	e.fakeDelegate(vA, uint64(4_000*ssErth), "feb")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "feb-b")
	e.days(2)
	janSupply := e.vsupply(vA, jan)
	require.True(t, janSupply.IsPositive())

	// Ten seconds before: still live.
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-10 * time.Second))
	_, retired := e.retiredVintage(vA, jan)
	require.False(t, retired)
	require.Equal(t, janSupply, e.vsupply(vA, jan))
	e.requireVintageBooks()

	// Past the boundary, January is retiring: every msg on it is refused
	// (by time), but its supply stays in the book, earning, until the epoch
	// end; it cannot be withdrawn yet.
	e.toTime(ret.Add(5 * time.Second))
	epoch, err := k.Epoch.Get(e.ctx())
	require.NoError(t, err)
	require.Greater(t, epoch.EndTime, e.now.Unix(), "mid-epoch")
	_, retired = e.retiredVintage(vA, jan)
	require.False(t, retired, "booked at the epoch end")
	require.Equal(t, janSupply, e.vsupply(vA, jan))
	_, err = e.tryFakeUndelegate(vA, 0, 1, "too-early")
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage)
	require.Contains(t, err.Error(), "next epoch end")
	q, err := sskeeper.NewQueryServerImpl(k).Vintages(e.ctx(), &sstypes.QueryVintagesRequest{Validator: e.valoper(vA)})
	require.NoError(t, err)
	require.Len(t, q.Retiring, 1)
	require.Equal(t, jan, q.Retiring[0].Vintage)
	ut, err := e.app.StakingKeeper.UnbondingTime(e.ctx())
	require.NoError(t, err)
	require.Equal(t, epoch.EndTime+int64(ut/time.Second), q.Retiring[0].ExpectedCompletion)
	e.requireVintageBooks()

	// The retirement's arithmetic, at the epoch end (in a throwaway cache):
	// V = floor(S_m x B / S) at the rate then, rewards since the boundary
	// included.
	{
		end := time.Unix(epoch.EndTime, 0)
		ctx, _ := e.app.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height + 1, Time: end}).CacheContext()
		b, s, err := k.Backing(ctx, e.valoper(vA))
		require.NoError(t, err)
		require.NoError(t, k.EndBlocker(ctx))
		rv, err := k.RetiredVintages.Get(ctx, collections.Join(e.valoper(vA), jan))
		require.NoError(t, err)
		want := janSupply.Mul(b).Quo(s)
		require.Equal(t, want, rv.Value, "V = floor(S_m x B / S)")
		require.True(t, rv.Value.Mul(s).LTE(janSupply.Mul(b)), "floored")
		require.Equal(t, epoch.Number, rv.Epoch, "the epoch that ended")
		require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, func() sstypes.UnbondStatus {
			r, err := k.UnbondRecords.Get(ctx, collections.Join(e.valoper(vA), rv.Epoch))
			require.NoError(t, err)
			return r.Status
		}(), "undelegated in the step that booked it")
		// The live holders' rate does not fall: (B - V) / (S - S_m) >= B / S.
		require.True(t, b.Sub(want).Mul(s).GTE(b.Mul(s.Sub(janSupply))))
	}

	e.toTime(time.Unix(epoch.EndTime, 0))
	rv, retired := e.retiredVintage(vA, jan)
	require.True(t, retired)
	require.Equal(t, janSupply, rv.Supply)
	require.Equal(t, rv.Supply, rv.RemainingSupply)
	require.Equal(t, rv.Value, rv.RemainingValue)
	require.True(t, rv.Value.IsPositive())
	held, err := k.UnbondPayouts.Get(e.ctx(), rv.PayoutId)
	require.NoError(t, err)
	require.Empty(t, held.Pc, "held: no pc")
	require.Equal(t, rv.Value, held.Value)
	require.True(t, e.vsupply(vA, jan).IsZero())
	require.Equal(t, e.vsupply(vA, feb), e.state(vA).DerthSupply)
	require.Equal(t, rv.Value, e.record(vA, rv.Epoch).Requested)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vA, rv.Epoch).Status)
	require.Len(t, eventsOf(e.events, sstypes.EventTypeVintageRetired), 1, "vB's February vintage is still live")
	q, err = sskeeper.NewQueryServerImpl(k).Vintages(e.ctx(), &sstypes.QueryVintagesRequest{Validator: e.valoper(vA)})
	require.NoError(t, err)
	require.Empty(t, q.Retiring)
	require.Len(t, q.Retired, 1)
	require.Equal(t, e.record(vA, rv.Epoch).CompletionTime/int64(time.Second), q.Retired[0].ExpectedCompletion)
	e.requireVintageBooks()

	// Every msg but a withdrawal is refused on the retired asset.
	srv := e.stakeSrv()
	rs := &sstypes.MsgRestake{Validator: e.valoper(vA), Stake: fakeStake("r-restake", false)}
	_, err = srv.Restake(e.fakeAuthorized(rs), rs)
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage)
	_, err = e.tryFakeRedelegateFrom(vA, vB, 0, uint64(ssErth), "r-redelegate")
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage)
	_, err = e.tryFakeCheckIn(e.fakeCheckInMsg(vA, jan, uint64(ssErth), "r-checkin"))
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage)
	dm := fakeDelegateMsg(e.valoper(vA), uint64(ssErth), "r-delegate")
	dm.Derth, dm.Vintage = e.exactDerth(vA, dm.Amount), jan
	_, err = srv.Delegate(e.fakeAuthorized(dm), dm)
	require.ErrorIs(t, err, sstypes.ErrVintage)
	split := &sstypes.MsgUndelegate{Validator: e.valoper(vA), Amount: 1, Stake: fakeStake("r-split", false),
		Pc: privacy.FieldBytes(ssDet("pc", 9)), Ciphertext: shieldedtest.BlindCT("r-split"),
		GroundworksSplit: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}}
	_, err = srv.Undelegate(e.fakeAuthorized(split), split)
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage, "a withdrawal does not vote")
	_, err = e.tryFakeUndelegate(vA, 0, rv.Supply.Uint64()+1, "r-too-much")
	require.ErrorIs(t, err, sstypes.ErrAmount)

	// 1. A third, while unbonding.
	d1 := rv.Supply.QuoRaw(3)
	w1 := d1.Mul(rv.Value).Quo(rv.Supply)
	res1, err := e.tryFakeUndelegate(vA, 0, d1.Uint64(), "w1")
	require.NoError(t, err)
	require.Equal(t, w1.Uint64(), res1.Value)
	p1, err := k.UnbondPayouts.Get(e.ctx(), res1.PayoutId)
	require.NoError(t, err)
	require.Equal(t, w1, p1.Value)
	rv, _ = e.retiredVintage(vA, jan)
	require.Equal(t, rv.Supply.Sub(d1), rv.RemainingSupply)
	require.Equal(t, rv.Value.Sub(w1), rv.RemainingValue)
	e.next(5 * time.Second)
	e.requireVintageBooks()

	// 2. Half of the rest, a day on.
	e.days(1)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vA, rv.Epoch).Status)
	d2 := rv.RemainingSupply.QuoRaw(2)
	w2 := d2.Mul(rv.RemainingValue).Quo(rv.RemainingSupply)
	res2, err := e.tryFakeUndelegate(vA, jan, d2.Uint64(), "w2") // named by its month: the same asset
	require.NoError(t, err)
	require.Equal(t, w2.Uint64(), res2.Value)
	e.next(5 * time.Second)
	e.requireVintageBooks()

	// Maturity: both paid at value x payout / requested.
	e.days(22)
	r := e.record(vA, rv.Epoch)
	require.Equal(t, sstypes.UNBOND_STATUS_MATURED, r.Status)
	for _, c := range []struct {
		id uint64
		w  math.Int
	}{{res1.PayoutId, w1}, {res2.PayoutId, w2}} {
		ev := e.payoutEvents(sstypes.EventTypeUnbondPayout, c.id)
		require.Len(t, ev, 1)
		require.Equal(t, c.w.Mul(r.Payout).Quo(r.Requested).String(), ev[0]["amount"])
	}
	e.requireVintageBooks()

	// 3. The last derth, after maturity: the whole remainder, paid in the
	// next block; the vintage, its held payout and the record are done.
	rv, _ = e.retiredVintage(vA, jan)
	res3, err := e.tryFakeUndelegate(vA, 0, rv.RemainingSupply.Uint64(), "w3")
	require.NoError(t, err)
	require.Equal(t, rv.RemainingValue.Uint64(), res3.Value)
	_, retired = e.retiredVintage(vA, jan)
	require.False(t, retired, "withdrawn whole")
	_, err = k.UnbondPayouts.Get(e.ctx(), rv.PayoutId)
	require.ErrorIs(t, err, collections.ErrNotFound, "the held payout is spent")
	e.next(5 * time.Second)
	ev := e.payoutEvents(sstypes.EventTypeUnbondPayout, res3.PayoutId)
	require.Len(t, ev, 1)
	require.Equal(t, rv.RemainingValue.Mul(r.Payout).Quo(r.Requested).String(), ev[0]["amount"])
	_, err = k.UnbondRecords.Get(e.ctx(), collections.Join(e.valoper(vA), rv.Epoch))
	require.ErrorIs(t, err, collections.ErrNotFound, "every payout made: the record is done")
	_, err = e.tryFakeUndelegate(vA, 0, 1, "w4")
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage, "nothing left")
	e.requireVintageBooks()
}

// After a halt every month due retires in the first block: two vintages of
// one book in one BeginBlock; the next month's stays live.
func TestVintageRetirementHaltCatchUp(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	var months []uint32
	for i := int64(0); i < 3; i++ {
		m := sstypes.AddMonths(jan, i)
		if i > 0 {
			e.toTime(sstypes.VintageStart(m).Add(time.Hour))
		}
		e.fakeDelegate(vA, uint64(1_000*ssErth)*uint64(i+1), fmt.Sprintf("m%d", i))
		months = append(months, m)
	}
	e.days(1)
	// Halted from before January's retirement until after February's.
	e.toTime(sstypes.VintageRetiresAt(months[1]).Add(3 * time.Hour))
	evs := eventsOf(e.events, sstypes.EventTypeVintageRetired)
	require.Len(t, evs, 2)
	for i, m := range months[:2] {
		require.Equal(t, strconv.Itoa(int(m)), evs[i]["vintage"], "oldest first")
		_, ok := e.retiredVintage(vA, m)
		require.True(t, ok)
	}
	_, ok := e.retiredVintage(vA, months[2])
	require.False(t, ok)
	require.Equal(t, e.vsupply(vA, months[2]), e.state(vA).DerthSupply)
	sweep, err := e.app.ShieldedStakingKeeper.VintageSweep.Get(e.ctx())
	require.NoError(t, err)
	require.False(t, sweep.Active)
	require.Equal(t, e.month(), sweep.Month)
	e.requireVintageBooks()
	// Both withdraw from the same record (one epoch).
	r1, _ := e.retiredVintage(vA, months[0])
	r2, _ := e.retiredVintage(vA, months[1])
	require.Equal(t, r1.Epoch, r2.Epoch)
	require.Equal(t, r1.Value.Add(r2.Value), e.record(vA, r1.Epoch).Requested)

	q, err := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Vintages(e.ctx(),
		&sstypes.QueryVintagesRequest{Validator: e.valoper(vA)})
	require.NoError(t, err)
	require.Len(t, q.Retired, 2)
	require.Len(t, q.Live, 1)
	require.NotNil(t, q.Retired[0].Record)
	require.Equal(t, sstypes.DerthDenom(e.valoper(vA)), q.Retired[0].Denom)
}

// A slash cuts a retired vintage's value as it cuts any stake: while the
// vintage is retiring (still in its book: the rate falls, and the epoch end
// books less) and while its held value unbonds (the SDK entry). The
// withdrawals shrink with it.
func TestVintageRetiredThroughSlashes(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	for _, v := range []sdk.ValAddress{vA, vB} {
		e.fakeDelegate(v, uint64(10_000*ssErth), "jan-"+e.valoper(v))
	}
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	for _, v := range []sdk.ValAddress{vA, vB} {
		e.fakeDelegate(v, uint64(5_000*ssErth), "feb-"+e.valoper(v)) // live holders too
	}
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-time.Hour)) // an epoch end
	e.next(5 * time.Second)
	infraction := e.height
	e.toTime(ret.Add(time.Minute)) // both retiring: still in their books
	_, ok := e.retiredVintage(vA, jan)
	require.False(t, ok)

	// vA is slashed while January is retiring: the slash lowers its rate,
	// so the value the epoch end books for January is cut with the rest.
	bA, sA, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	janA := e.vsupply(vA, jan)
	e.doubleSign(vA, infraction)
	bA2, sA2, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(vA))
	require.NoError(t, err)
	require.Equal(t, sA, sA2)
	require.True(t, bA2.LT(bA), "the slash cut the book")
	e.days(1) // the epoch end books both
	rvA, ok := e.retiredVintage(vA, jan)
	require.True(t, ok)
	rvB, ok := e.retiredVintage(vB, jan)
	require.True(t, ok)
	require.True(t, rvA.Value.LT(janA.Mul(bA).Quo(sA)), "booked after the slash: %s", rvA.Value)
	// vB's record is unbonding; then vB is slashed for an infraction before
	// the entry: the entry is cut.
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vB, rvB.Epoch).Status)
	e.next(5 * time.Second)
	e.doubleSign(vB, e.height-2)
	e.requireVintageBooks()

	// Each withdraws whole before maturity; at maturity each is paid its
	// share of what the slashed record paid.
	resA, err := e.tryFakeUndelegate(vA, 0, rvA.Supply.Uint64(), "a")
	require.NoError(t, err)
	resB, err := e.tryFakeUndelegate(vB, 0, rvB.Supply.Uint64(), "b")
	require.NoError(t, err)
	require.Equal(t, rvA.Value.Uint64(), resA.Value)
	e.days(23)
	for _, c := range []struct {
		val sdk.ValAddress
		rv  sstypes.RetiredVintage
		id  uint64
	}{{vA, rvA, resA.PayoutId}, {vB, rvB, resB.PayoutId}} {
		ev := e.payoutEvents(sstypes.EventTypeUnbondPayout, c.id)
		require.Len(t, ev, 1, e.valoper(c.val))
		paid, ok := math.NewIntFromString(ev[0]["amount"])
		require.True(t, ok)
		if c.val.Equals(vA) {
			// Slashed before it was booked: paid what was booked, but for
			// x/staking's rounding.
			require.True(t, paid.GTE(c.rv.Value.SubRaw(2)) && paid.LTE(c.rv.Value), "%s of %s", paid, c.rv.Value)
			continue
		}
		require.True(t, paid.LT(c.rv.Value), "%s: the slash reached the held value (%s of %s)", e.valoper(c.val), paid, c.rv.Value)
		require.True(t, paid.GT(c.rv.Value.MulRaw(90).QuoRaw(100)), "a 5%% slash")
	}
	e.requireVintageBooks()
}

// ---- Groundworks ------------------------------------------------------------------

// Groundworks totals are kept per vintage; a validator's voter is their sum
// over the live vintages. A vintage's retirement drops its totals, the voter
// is re-filed without them, and its dead votes are deleted.
func TestVintageGroundworksTotals(t *testing.T) {
	g := initGwEnv(t)
	k := g.app.ShieldedStakingKeeper
	jan := g.month()
	a := g.castGw(g.v, 1_000*gwE, g.split(50, 50))
	require.Equal(t, jan, g.storedVote(a.id).Vintage)
	feb := sstypes.AddMonths(jan, 1)
	g.toTime(sstypes.VintageStart(feb).Add(time.Hour))

	// A check-in whose output votes: the vote is in February's vintage.
	m := g.fakeCheckInMsg(g.v, jan, 3_000*gwE, "gw")
	tag := privacy.FieldBytes(ssDet("gw-checkin-tag", 1))
	m.Stake.CreditVoteTag, m.Stake.CreditVoteWeight = tag, 3_000*gwE
	m.GroundworksSplit = g.split(0, 100)
	_, err := g.tryFakeCheckIn(m)
	require.NoError(t, err)
	id, err := k.GwVotesByTag.Get(g.ctx(), tag)
	require.NoError(t, err)
	require.Equal(t, feb, g.storedVote(id).Vintage)
	tJan, err := k.GwTotals.Get(g.ctx(), collections.Join3(g.valoper(g.v), jan, g.opts[0]))
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(1_000*gwE).MulRaw(50), tJan)
	tFeb, err := k.GwTotals.Get(g.ctx(), collections.Join3(g.valoper(g.v), feb, g.opts[1]))
	require.NoError(t, err)
	require.Equal(t, math.NewIntFromUint64(3_000*gwE).MulRaw(100), tFeb)
	g.next(5 * time.Second)
	g.requireEquivalent(g.v, 2)
	require.Len(t, g.voter(g.v), 2)

	// January retires: its totals go and the voter is re-filed (EndBlock);
	// January's dead vote is deleted (BeginBlock's sweep).
	g.toTime(sstypes.VintageRetiresAt(jan).Add(time.Minute))
	_, err = k.GwTotals.Get(g.ctx(), collections.Join3(g.valoper(g.v), jan, g.opts[0]))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.NotContains(t, g.voter(g.v), g.opts[0], "January's weight left the voter")
	require.Contains(t, g.voter(g.v), g.opts[1])
	feb3 := g.state(g.v).EpochRate.MulInt(math.NewIntFromUint64(3_000 * gwE).MulRaw(100)).QuoInt64(100).TruncateInt()
	require.Equal(t, feb3, g.voter(g.v)[g.opts[1]], "only February's 3,000 derth counts on it")
	require.False(t, g.hasVote(a.id), "the dead vote is swept")
	require.True(t, g.hasVote(id))
	retiredEv := 0
	for _, ev := range eventsOf(g.events, sstypes.EventTypeGroundworksVote) {
		if ev["action"] == "retired" {
			retiredEv++
		}
	}
	require.Equal(t, 1, retiredEv)
	g.requireEquivalent(g.v, 1)

	// February retires too: no live totals, no voter.
	g.toTime(sstypes.VintageRetiresAt(feb).Add(time.Minute))
	require.Nil(t, g.voter(g.v))
	require.False(t, g.hasVote(id))
	_, err = k.GwEpoch.Get(g.ctx(), g.valoper(g.v))
	require.ErrorIs(t, err, collections.ErrNotFound, "the validator left the Groundworks index")
	require.NoError(t, k.AssertInvariants(g.ctx()))
}

// ---- gov -------------------------------------------------------------------------

// A stake vote names its notes' vintage: refused once it has retired, or
// when it is after the snapshot's month. The snapshot's supply counts the
// live vintages only; a vote cast before its vintage retired keeps counting.
func TestVintageStakeVotes(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	e.fakeDelegate(vB, uint64(10_000*ssErth), "jan")
	feb := sstypes.AddMonths(jan, 1)
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	e.fakeDelegate(vB, uint64(2_000*ssErth), "feb")

	vote := func(pid uint64, vintage uint32, label string) error {
		m := &sstypes.MsgStakeVote{ProposalId: pid, Validator: e.valoper(vB), Weight: 1_000,
			Options:        []*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: "1.000000000000000000"}},
			VoteNullifiers: [][]byte{privacy.FieldBytes(ssDet("vnf/"+label, 0)), privacy.FieldBytes(ssDet("vnf/"+label, 1))},
			Vintage:        vintage}
		root, _, err := e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
		require.NoError(t, err)
		m.DebtRoot = root
		_, err = e.stakeSrv().StakeVote(e.fakeAuthorized(m), m)
		return err
	}

	// Snapshot on the last day of the month before January's retirement.
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-2 * time.Hour))
	e.shield(uint64(100 * ssErth))
	pid := e.submitProposal()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), pid)
	require.NoError(t, err)
	require.Equal(t, e.month(), snap.Vintage)
	before, err := e.app.ShieldedStakingKeeper.SnapshotSupply(e.ctx(), pid, e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, e.state(vB).DerthSupply, before)
	require.NoError(t, vote(pid, 0, "jan-early"), "January is live: it votes")
	tally0, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(pid, e.valoper(vB)))
	require.NoError(t, err)

	e.toTime(ret.Add(time.Minute)) // January retires
	require.ErrorIs(t, vote(pid, jan, "jan-late"), sstypes.ErrRetiredVintage)
	require.ErrorIs(t, vote(pid, 0, "jan-late0"), sstypes.ErrRetiredVintage)
	require.NoError(t, vote(pid, feb, "feb"))
	require.ErrorIs(t, vote(pid, e.month(), "after"), sstypes.ErrVintage, "a vintage after the snapshot's month")
	tally1, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(pid, e.valoper(vB)))
	require.NoError(t, err)
	require.True(t, tally1.Yes.GT(tally0.Yes), "January's vote still counts beside February's")
	after, err := e.app.ShieldedStakingKeeper.SnapshotSupply(e.ctx(), pid, e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, before, after, "the snapshot keeps the supply it was taken at")

	// A proposal snapshotted now counts January too: retiring, its value is
	// still bonded in the module's delegation until the epoch end books it.
	pid2 := e.submitProposal()
	s2, err := e.app.ShieldedStakingKeeper.SnapshotSupply(e.ctx(), pid2, e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, e.state(vB).DerthSupply, s2)
	require.True(t, e.vsupply(vB, jan).IsPositive())
	// After the epoch end, the live vintages only.
	e.days(1)
	_, booked := e.retiredVintage(vB, jan)
	require.True(t, booked)
	pid3 := e.submitProposal()
	s3, err := e.app.ShieldedStakingKeeper.SnapshotSupply(e.ctx(), pid3, e.valoper(vB))
	require.NoError(t, err)
	require.Equal(t, e.vsupply(vB, feb), s3)
	e.requireVintageBooks()
}

// ---- genesis, upgrade, params ----------------------------------------------------

// Vintage supplies, retired vintages and their held payouts travel through
// an export and import, with every invariant.
func TestVintageGenesisRoundTrip(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(500_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	e.fakeDelegate(vA, uint64(10_000*ssErth), "jan")
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	e.fakeDelegate(vA, uint64(1_000*ssErth), "feb")
	e.toTime(sstypes.VintageRetiresAt(jan).Add(time.Minute))
	rv, _ := e.retiredVintage(vA, jan)
	_, err := e.tryFakeUndelegate(vA, 0, rv.Supply.QuoRaw(4).Uint64(), "part")
	require.NoError(t, err)
	e.next(5 * time.Second)

	fresh, fctx := e.exportImport()
	fk := fresh.ShieldedStakingKeeper
	got, err := fk.RetiredVintages.Get(fctx, collections.Join(e.valoper(vA), jan))
	require.NoError(t, err)
	want, _ := e.retiredVintage(vA, jan)
	require.Equal(t, want, got)
	base, err := fk.BaseVintage.Get(fctx)
	require.NoError(t, err)
	require.Equal(t, jan, base)
	s, err := fk.VintageSupply(fctx, e.valoper(vA), sstypes.AddMonths(jan, 1))
	require.NoError(t, err)
	require.Equal(t, e.vsupply(vA, sstypes.AddMonths(jan, 1)), s)
	held, err := fk.UnbondPayouts.Get(fctx, got.PayoutId)
	require.NoError(t, err)
	require.Empty(t, held.Pc)
	has, err := fk.PayoutsByRecord.Has(fctx, collections.Join3(held.Validator, held.Epoch, held.Id))
	require.NoError(t, err)
	require.False(t, has, "a held payout is queued nowhere after import")
	require.NoError(t, fk.AssertInvariants(fctx))
}

// The v1.3.0 handler on pre-vintage state (votes with leases, the lease
// queue, per-(validator, option) totals, no vintage supplies, moves without
// a vintage): the upgrade month becomes the base vintage holding every
// book's supply, votes join it without a lease, totals move per vintage
// with the voters unchanged, and the chain goes on.
func TestUpgradeV1_3_0(t *testing.T) {
	g := initGwEnv(t)
	k := g.app.ShieldedStakingKeeper
	vB, _ := g.createValidator(1000 * ssErth)
	g.next(5 * time.Second)
	a := g.castGw(g.v, 1_000*gwE, g.split(60, 40))
	b := g.castGw(g.v, 500*gwE, g.split(0, 0, 100))
	// The upgrade lands in a later month than the chain's first, with a move
	// still open.
	g.toTime(sstypes.VintageStart(sstypes.AddMonths(g.month(), 2)).Add(3 * time.Hour))
	_, err := g.fakeRedelegate(g.v, vB, uint64(1_000*gwE), "upgrade")
	require.NoError(t, err)
	g.next(5 * time.Second)
	voters := g.voter(g.v)
	require.NotEmpty(t, voters)

	// Rewind the store to v1.2.2's shape.
	ctx := g.ctx()
	sb := collections.NewSchemaBuilder(runtimeKV(g.app))
	lapses := collections.NewKeySet(sb, sstypes.GwLapsesKey, "gw_lapses", collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key))
	oldTotals := collections.NewMap(sb, sstypes.GwTotalsKey, "gw_totals",
		collections.PairKeyCodec(collections.StringKey, collections.Uint64Key), sdk.IntValue)
	var votes []sstypes.GroundworksVote
	require.NoError(t, k.GwVotes.Walk(ctx, nil, func(_ uint64, v sstypes.GroundworksVote) (bool, error) {
		votes = append(votes, v)
		return false, nil
	}))
	// Stored as v1.2.2 wrote them: no vintage, a lease in field 11 (now
	// reserved, so appended by hand: tag 11<<3|varint).
	rawVotes := collections.NewMap(sb, sstypes.GwVotesKey, "gw_votes_raw", collections.Uint64Key, collections.BytesValue)
	for _, v := range votes {
		v.Vintage = 0
		bz, err := v.Marshal()
		require.NoError(t, err)
		lease := g.now.Unix() + 86400
		bz = binary.AppendUvarint(append(bz, 11<<3), uint64(lease))
		require.NoError(t, rawVotes.Set(ctx, v.Id, bz))
		require.NoError(t, lapses.Set(ctx, collections.Join(lease, v.Id)))
	}
	require.NoError(t, k.GwVotesByVintage.Clear(ctx, nil))
	require.NoError(t, k.GwTotals.Walk(ctx, nil, func(key collections.Triple[string, uint32, uint64], t0 math.Int) (bool, error) {
		cur, err := oldTotals.Get(ctx, collections.Join(key.K1(), key.K3()))
		if err != nil {
			cur = math.ZeroInt()
		}
		return false, oldTotals.Set(ctx, collections.Join(key.K1(), key.K3()), cur.Add(t0))
	}))
	require.NoError(t, k.GwTotals.Clear(ctx, nil))
	require.NoError(t, k.VintageSupplies.Clear(ctx, nil))
	require.NoError(t, k.BaseVintage.Remove(ctx))
	require.NoError(t, k.VintageSweep.Remove(ctx))
	var moves []sstypes.Move
	require.NoError(t, k.Moves.Walk(ctx, nil, func(_ []byte, mv sstypes.Move) (bool, error) {
		moves = append(moves, mv)
		return false, nil
	}))
	require.NotEmpty(t, moves)
	for _, mv := range moves {
		mv.Vintage = 0
		require.NoError(t, k.Moves.Set(ctx, mv.Key, mv))
	}

	_, err = v1_3_0Handler(g.app)(ctx, upgradetypes.Plan{Name: UpgradeV1_3_0}, g.app.ModuleManager.GetVersionMap())
	require.NoError(t, err)

	up := sstypes.MonthOf(g.now)
	base, err := k.BaseVintage.Get(g.ctx())
	require.NoError(t, err)
	require.Equal(t, up, base, "the base vintage is the upgrade month")
	for _, v := range []sdk.ValAddress{g.v, vB} {
		require.Equal(t, g.state(v).DerthSupply, g.vsupply(v, up), "every book's supply is the base vintage's")
	}
	for _, id := range []uint64{a.id, b.id} {
		v := g.storedVote(id)
		require.Equal(t, up, v.Vintage)
	}
	n := 0
	require.NoError(t, lapses.Walk(g.ctx(), nil, func(collections.Pair[int64, uint64]) (bool, error) { n++; return false, nil }))
	require.Zero(t, n, "the lease queue is gone")
	require.NoError(t, oldTotals.Walk(g.ctx(), nil, func(collections.Pair[string, uint64], math.Int) (bool, error) { n++; return false, nil }))
	require.Zero(t, n, "the old totals are gone")
	for _, mv := range moves {
		got, ok := g.move(mv.Key)
		require.True(t, ok)
		require.Equal(t, up, got.Vintage)
	}

	g.next(5 * time.Second)
	require.Equal(t, voters, g.voter(g.v), "the voter weighs what it did")
	g.requireEquivalent(g.v, 2)
	g.requireVintageBooks()
	// The chain goes on: an epoch, a vote, the legacy asset is the current
	// vintage's (the base) and retires 13 months on.
	g.days(1)
	g.castGw(g.v, 700*gwE, g.split(100))
	g.toTime(sstypes.VintageRetiresAt(up).Add(time.Minute))
	_, ok := g.retiredVintage(g.v, up)
	require.True(t, ok)
	g.requireVintageBooks()
}

// x/staking's unbonding_time stays at most a year less a day: a gov
// MsgUpdateParams above it is refused on the router every proposal
// executes through, and genesis refuses one.
func TestVintageUnbondingTimeBound(t *testing.T) {
	e := initStakeEnv(t)
	ctx, _ := e.ctx().CacheContext()
	p, err := e.app.StakingKeeper.GetParams(ctx)
	require.NoError(t, err)
	gov := e.bech(authtypes.NewModuleAddress("gov"))
	run := func(ut time.Duration) error {
		p.UnbondingTime = ut
		msg := &stakingtypes.MsgUpdateParams{Authority: gov, Params: p}
		h := e.app.rewardsRouter.Handler(msg)
		require.NotNil(t, h)
		_, err := h(ctx, msg)
		return err
	}
	require.ErrorContains(t, run(365*24*time.Hour), "unbonding_time")
	require.NoError(t, run(sstypes.MaxUnbondingSeconds*time.Second))

	refused := func() (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		_, err := initStakeEnvWith(t, func(appState map[string]json.RawMessage, _ sdk.AccAddress) {
			var st map[string]any
			require.NoError(t, json.Unmarshal(appState[stakingtypes.ModuleName], &st))
			st["params"].(map[string]any)["unbonding_time"] = "31536000s"
			bz, err := json.Marshal(st)
			require.NoError(t, err)
			appState[stakingtypes.ModuleName] = bz
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}()
	require.Contains(t, refused, "unbonding_time", "genesis refuses a year's unbonding")
}

// ---- property ------------------------------------------------------------------------

// Random stake msgs over 30 months, two validators: after every block each
// book's derth_supply is the sum of its live vintages' supplies, and every
// invariant holds (the module's ERTH is exactly what its books owe).
func TestVintageSupplyProperty(t *testing.T) {
	for _, seed := range []int64{1, 2} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			e := initStakeEnv(t)
			e.fundPoolDirect(1_000_000_000 * ssErth)
			vals := []sdk.ValAddress{}
			for i := 0; i < 2; i++ {
				v, _ := e.createValidator(1000 * ssErth)
				vals = append(vals, v)
			}
			e.next(5 * time.Second)
			k := e.app.ShieldedStakingKeeper
			n := 0
			label := func() string { n++; return fmt.Sprintf("p%d-%d", seed, n) }
			pick := func(v sdk.ValAddress) (uint32, uint64) {
				var ms []uint32
				var ss []uint64
				require.NoError(t, k.VintageSupplies.Walk(e.ctx(), collections.NewPrefixedPairRange[string, uint32](e.valoper(v)),
					func(key collections.Pair[string, uint32], s math.Int) (bool, error) {
						if sstypes.VintageLive(key.K2(), e.month()) {
							ms, ss = append(ms, key.K2()), append(ss, s.Uint64())
						}
						return false, nil
					}))
				if len(ms) == 0 {
					return 0, 0
				}
				i := rng.Intn(len(ms))
				return ms[i], ss[i]
			}
			for step := 0; step < 120; step++ {
				v := vals[rng.Intn(len(vals))]
				m, s := pick(v)
				switch op := rng.Intn(7); {
				case op == 0 || s == 0:
					// 2 to 2,000 derth at the live rate (rewards over months
					// raise it far above 1).
					rate := e.rate(v)
					amt := rate.MulInt64(rng.Int63n(1_998*ssErth) + 2*ssErth).Ceil().TruncateInt()
					e.fakeDelegate(v, amt.Uint64(), label())
				case op == 1:
					_, err := e.tryFakeUndelegate(v, m, uint64(rng.Int63n(int64(s)))+1, label())
					require.NoError(t, err)
				case op == 2 && m != e.month():
					_, err := e.tryFakeCheckIn(e.fakeCheckInMsg(v, m, uint64(rng.Int63n(int64(s)))+1, label()))
					require.NoError(t, err)
				case op == 3:
					amt := uint64(rng.Int63n(int64(s))) + 1
					if math.NewIntFromUint64(amt).GTE(math.NewInt(2 * ssErth)) {
						dst := vals[0]
						if dst.Equals(v) {
							dst = vals[1]
						}
						if _, err := e.tryFakeRedelegateFrom(v, dst, m, amt, label()); err != nil {
							require.ErrorIs(t, err, sstypes.ErrAmount, "only a credit below the minimum")
						}
					}
				case op == 4:
					// Withdraw from a retired vintage, if any.
					require.NoError(t, k.RetiredVintages.Walk(e.ctx(), nil, func(key collections.Pair[string, uint32], rv sstypes.RetiredVintage) (bool, error) {
						val, err := sdk.ValAddressFromBech32(rv.Validator)
						require.NoError(t, err)
						d := uint64(rng.Int63n(rv.RemainingSupply.Int64())) + 1
						_, err = e.tryFakeUndelegate(val, rv.Vintage, d, label())
						require.NoError(t, err)
						return true, nil
					}))
				default:
					// Time: a day, or a month or more.
					if rng.Intn(3) == 0 {
						e.toTime(sstypes.VintageStart(sstypes.AddMonths(e.month(), int64(1+rng.Intn(4)))).Add(time.Duration(rng.Intn(3600)) * time.Second))
					} else {
						e.days(1)
					}
				}
				e.next(5 * time.Second)
				e.requireVintageBooks()
			}
			require.NotEmpty(t, eventsOf(e.events, sstypes.EventTypeVintageRetired), "the run retired vintages")
			require.Greater(t, sstypes.VintageIndex(e.month())-sstypes.VintageIndex(sstypes.MonthOf(ssGenesisTime)), int64(13))
		})
	}
}

// A slash of a redelegation's source books its debt on the vintage the
// move's credit landed in at the destination (dst's current month at the
// move), not on the destination's other vintages.
func TestVintageSlashDebtPerVintage(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(50_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	jan := e.month()
	e.fakeDelegate(vA, uint64(4_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(2_000*ssErth), "b")
	feb := sstypes.AddMonths(jan, 1)
	e.toTime(sstypes.VintageStart(feb).Add(time.Hour))
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	// January's notes at A move to B: the credit lands in B's February.
	r, err := e.tryFakeRedelegateFrom(vA, vB, jan, uint64(1_000*ssErth), "ab")
	require.NoError(t, err)
	mv, ok := e.move(fakeMoveKey("ab"))
	require.True(t, ok)
	require.Equal(t, feb, mv.Vintage)
	require.Equal(t, math.NewIntFromUint64(r.Derth), e.vsupply(vB, feb))
	e.next(5 * time.Second)
	janB, febB := e.vsupply(vB, jan), e.vsupply(vB, feb)

	res := e.doubleSign(vA, infraction)
	evs := eventsOf(res.Events, sstypes.EventTypeSlashDebt)
	require.Len(t, evs, 1)
	debt := evInt(t, evs[0], "debt")
	require.True(t, debt.IsPositive())
	require.Equal(t, febB.Sub(debt), e.vsupply(vB, feb), "February's vintage owes the debt")
	require.Equal(t, janB, e.vsupply(vB, jan), "January's is untouched")
	e.requireVintageBooks()
}
