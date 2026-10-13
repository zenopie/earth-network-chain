package app

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/stretchr/testify/require"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// Stake vintages, audit 2 fix pass.

// L1. A booking is committed only with its undelegation: when
// processValidator fails at the epoch end, January's booking rolls back and
// it stays in the book (S unchanged, nothing pending), earning for its own
// holders; the book's next successful epoch end books and undelegates it.
func TestVintageBookingRollsBackWithUndelegation(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	jan := e.month()
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.toTime(sstypes.VintageStart(sstypes.AddMonths(jan, 1)).Add(time.Hour))
	e.fakeDelegate(vA, uint64(1_000*ssErth), "feb") // a partial retirement
	ret := sstypes.VintageRetiresAt(jan)
	e.toTime(ret.Add(-time.Hour)) // an epoch end
	e.toTime(ret.Add(time.Minute))
	ep, err := k.Epoch.Get(e.ctx())
	require.NoError(t, err)
	janS := e.vsupply(vA, jan)
	require.True(t, janS.IsPositive())
	before := e.state(vA)

	// A pending index entry of an old epoch with no record: processValidator
	// fails for vA (pendingRecordsUpTo), after retireDue would have booked.
	stray := collections.Join(e.valoper(vA), uint64(0))
	require.NoError(t, k.PendingRecords.Set(e.ctx(), stray))
	n0 := len(e.events)
	e.toTime(time.Unix(ep.EndTime, 0))

	_, retired := e.retiredVintage(vA, jan)
	require.False(t, retired, "the booking rolled back with the undelegation")
	require.Equal(t, janS, e.vsupply(vA, jan), "still in the book, earning")
	after := e.state(vA)
	require.Equal(t, before.DerthSupply, after.DerthSupply)
	require.True(t, after.PendingUndelegation.IsZero(), "nothing booked out of the bond")
	_, err = k.UnbondRecords.Get(e.ctx(), collections.Join(e.valoper(vA), ep.Number))
	require.ErrorIs(t, err, collections.ErrNotFound, "no record for the booking")
	stages := map[string]bool{}
	for _, f := range eventsOf(e.events[n0:], sstypes.EventTypeEpochFailure) {
		stages[f["stage"]] = true
	}
	require.True(t, stages["validator"], "processValidator alone failed too: the whole book deferred")
	_, err = e.tryFakeUndelegate(vA, jan, 1, "retiring")
	require.ErrorIs(t, err, sstypes.ErrRetiredVintage, "still retiring: no withdrawal yet")

	// Repaired: the next epoch end books and undelegates January together.
	require.NoError(t, k.PendingRecords.Remove(e.ctx(), stray))
	e.days(1)
	rv, ok := e.retiredVintage(vA, jan)
	require.True(t, ok, "booked at the next epoch end")
	require.Equal(t, janS, rv.Supply)
	require.True(t, rv.Value.IsPositive())
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vA, rv.Epoch).Status, "undelegated in the same unit")
	require.True(t, e.vsupply(vA, jan).IsZero())
	e.requireVintageBooks()
}

// I2. Genesis max_clear_before is bounded by genesis time less the minimum
// label window: a value a proof could have named imports, one above it
// (never named; it would class in-window moves as late) is refused.
func TestVintageGenesisMaxClearBeforeBound(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.next(5 * time.Second)
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	gs, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)

	now := uint64(e.now.Unix())
	load := func(mcb uint64) error {
		var appState map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(exported.AppState, &appState))
		g := *gs
		g.MaxClearBefore = mcb
		bz, err := e.app.AppCodec().MarshalJSON(&g)
		require.NoError(t, err)
		appState[sstypes.ModuleName] = bz
		fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
			baseapp.SetChainID(ssChainID))
		fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
		return func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			if _, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState); err != nil {
				return err
			}
			got, err := fresh.ShieldedStakingKeeper.MaxClearBefore.Get(fctx)
			if err != nil {
				return err
			}
			if got != mcb {
				return fmt.Errorf("imported %d, want %d", got, mcb)
			}
			return nil
		}()
	}
	// Above ClearBefore(now) but below now less the slack: a window
	// lengthened by governance since the proof explains it; imported.
	require.NoError(t, load(now-sstypes.MoveTimeSlackSeconds))
	require.NoError(t, load(now-30*24*3600))
	require.ErrorContains(t, load(now-sstypes.MoveTimeSlackSeconds+1), "max_clear_before")
	require.ErrorContains(t, load(now+365*24*3600), "max_clear_before")
	require.ErrorContains(t, load(^uint64(0)), "max_clear_before")
}
