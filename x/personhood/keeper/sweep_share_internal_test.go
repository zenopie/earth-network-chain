package keeper

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// seedLapsed files n lapsed caretaker splits and n handles past their
// renewal period.
func seedLapsed(t *testing.T, k Keeper, ctx sdk.Context, n int) {
	t.Helper()
	at := ctx.BlockTime().Unix() - 1
	released := at - types.DefaultHandleRenewalSeconds
	for i := 0; i < n; i++ {
		nf := privacy.FieldBytes(privacy.U64(uint64(10_000 + i)))
		require.NoError(t, k.CaretakerExpiry.Set(ctx, collections.Join(at, nf)))
		require.NoError(t, k.putHandle(ctx, types.Handle{Handle: fmt.Sprintf("h%05d", i), Nullifier: nf, ExpiresAt: released,
			OwnerPk: make([]byte, 32), EkPub: make([]byte, 32)}))
	}
}

func countHandles(t *testing.T, k Keeper, ctx sdk.Context) int {
	t.Helper()
	n := 0
	require.NoError(t, k.HandleRelease.Walk(ctx, nil, func(collections.Pair[int64, string]) (bool, error) { n++; return false, nil }))
	return n
}

func countKeys(t *testing.T, ks collections.KeySet[collections.Pair[int64, []byte]], ctx sdk.Context) int {
	t.Helper()
	n := 0
	require.NoError(t, ks.Walk(ctx, nil, func(collections.Pair[int64, []byte]) (bool, error) { n++; return false, nil }))
	return n
}

// A revoked signer with far more registrations than one block can retire does
// not starve the other sweeps: expiry and handles each get their reserved
// share of the budget every block, and the total stays in budget. Caretaker
// leases are swept first on their own budget (CaretakerSweepLimit).
func TestLargePurgeDoesNotStarveOtherSweeps(t *testing.T) {
	k, _, ctx := capKeeper(t)
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	budget := params.RegistrationSweepLimitOrDefault()
	reserve := budget / sweepReserveDivisor
	require.Positive(t, reserve)

	now := ctx.BlockTime().Unix()
	for i := 0; i < 3*budget; i++ {
		seedReg(t, k, ctx, i, testDsc, now)
	}
	expiredAt := now - int64(params.RegistrationValiditySeconds) - 10
	for i := 0; i < 2*reserve; i++ {
		seedReg(t, k, ctx, 3*budget+i, []byte("other-signer"), expiredAt)
	}
	seedLapsed(t, k, ctx, 2*reserve)
	require.NoError(t, k.StartDscPurge(ctx, testDsc))

	beforeRegs := countRegistrations(t, k, ctx)
	require.NoError(t, k.BeginBlocker(ctx))

	var purged, expired int
	require.NoError(t, k.Registrations.Walk(ctx, nil, func(_ []byte, r types.Registration) (bool, error) {
		if string(r.DscKey) == string(testDsc) {
			purged++
		} else {
			expired++
		}
		return false, nil
	}))
	purged = 3*budget - purged
	expired = 2*reserve - expired
	caretaker := 2*reserve - countKeys(t, k.CaretakerExpiry, ctx)
	handles := 2*reserve - countHandles(t, k, ctx)

	require.GreaterOrEqual(t, expired, reserve, "expiry starved")
	require.Equal(t, 2*reserve, caretaker, "lapsed caretaker leases have a budget of their own (audit 4 C8)")
	require.GreaterOrEqual(t, handles, reserve, "handle sweep starved")
	require.Positive(t, purged)
	require.Equal(t, budget-3*reserve, purged, "the purge keeps the rest")
	require.LessOrEqual(t, purged+expired+handles, budget)
	require.Equal(t, beforeRegs-purged-expired, countRegistrations(t, k, ctx))
}

// With no purge backlog, the unused shares flow on: the expiry sweep can use
// the whole budget.
func TestUnusedSweepSharesFlowOn(t *testing.T) {
	k, _, ctx := capKeeper(t)
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	budget := params.RegistrationSweepLimitOrDefault()
	expiredAt := ctx.BlockTime().Unix() - int64(params.RegistrationValiditySeconds) - 10
	for i := 0; i < 2*budget; i++ {
		seedReg(t, k, ctx, i, []byte("other-signer"), expiredAt)
	}
	require.NoError(t, k.BeginBlocker(ctx))
	require.Equal(t, budget, countRegistrations(t, k, ctx))
}
