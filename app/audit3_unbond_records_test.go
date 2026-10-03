package app

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// AUDIT3 D: a validator's unbond records grow with every matured record
// nobody has claimed yet, and the epoch end walked all of them twice per
// validator (orphan sweep, orphan check). Orphans are indexed now: the epoch
// end's cost does not grow with a validator's unclaimed records.
func TestAudit3EpochEndCostIndependentOfUnclaimedRecords(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	e.delegate(vB, uint64(2_000*ssErth))
	e.days(1)
	valoper := e.valoper(vB)

	epochEndGas := func(n int) uint64 {
		cc, _ := e.ctx().CacheContext()
		for i := 0; i < n; i++ {
			r := sstypes.UnbondRecord{
				Validator: valoper, Epoch: 1_000_000 + uint64(i), Status: sstypes.UNBOND_STATUS_MATURED,
				Requested: math.OneInt(), Target: math.OneInt(), Undelegated: math.OneInt(),
				Payout: math.OneInt(), Outstanding: math.OneInt(), Paid: math.ZeroInt(),
			}
			require.NoError(t, e.app.ShieldedStakingKeeper.UnbondRecords.Set(cc, collections.Join(valoper, r.Epoch), r))
		}
		ep, err := e.app.ShieldedStakingKeeper.Epoch.Get(cc)
		require.NoError(t, err)
		ctx := cc.WithBlockTime(time.Unix(ep.EndTime, 0)).WithGasMeter(storetypes.NewGasMeter(1 << 60))
		require.NoError(t, e.app.ShieldedStakingKeeper.EndBlocker(ctx))
		return ctx.GasMeter().GasConsumed()
	}
	// Both past InvariantBookLimit, so the (bounded) invariant check costs
	// the same in each.
	few, many := epochEndGas(sstypes.InvariantBookLimit+100), epochEndGas(sstypes.InvariantBookLimit+5_100)
	t.Logf("epoch end gas: %d unclaimed records %d, %d records %d", sstypes.InvariantBookLimit+100, few,
		sstypes.InvariantBookLimit+5_100, many)
	require.InDelta(t, float64(few), float64(many), 5_000)
}
