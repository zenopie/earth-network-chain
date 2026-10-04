package keeper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 6 B6-5: a registration the expiry sweep cannot retire stays at the
// head of its index. It is passed over for SweepRetrySeconds, so the sweep
// reaches the registrations behind it, and tried again after.
func TestAudit6StuckSweepHeadIsPassedOver(t *testing.T) {
	k, ctx := regKeeper(t, nil)
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	params.RegistrationValiditySeconds = 1000
	require.NoError(t, k.Params.Set(ctx, params))
	t0 := ctx.BlockTime().Unix()

	// Two that cannot be retired (their leaf was never appended), registered
	// first, then one that can.
	for i := uint64(0); i < 2; i++ {
		require.NoError(t, k.addRegistration(ctx, types.Registration{
			Nullifier: privacy.FieldBytes(privacy.U64(100 + i)), LeafIndex: 1000 + i,
			RegisteredAt: t0 + int64(i), ActivatedAt: t0 + int64(i), Idc: privacy.FieldBytes(privacy.U64(1)),
		}))
	}
	leaf := privacy.U64(9)
	idx, err := k.appendLeaf(ctx, leaf)
	require.NoError(t, err)
	good := types.Registration{Nullifier: privacy.FieldBytes(privacy.U64(200)), LeafIndex: idx,
		RegisteredAt: t0 + 10, ActivatedAt: t0 + 10, Idc: privacy.FieldBytes(privacy.U64(2))}
	require.NoError(t, k.addRegistration(ctx, good))

	later := ctx.WithBlockTime(time.Unix(t0+5000, 0))
	// Budget 2: both stuck entries fail and are put aside.
	_, err = k.sweepExpiredRegistrations(later, 2)
	require.NoError(t, err)
	ok, err := k.Registrations.Has(later, good.Nullifier)
	require.NoError(t, err)
	require.True(t, ok)
	for i := uint64(0); i < 2; i++ {
		waiting, err := k.sweepRetrying(later, privacy.FieldBytes(privacy.U64(100+i)), t0+5000)
		require.NoError(t, err)
		require.True(t, waiting)
	}
	// The next sweep passes over them and retires the one behind.
	_, err = k.sweepExpiredRegistrations(later, 2)
	require.NoError(t, err)
	ok, err = k.Registrations.Has(later, good.Nullifier)
	require.NoError(t, err)
	require.False(t, ok, "the sweep reached past the stuck head")

	// After the retry time they are tried again (and fail again).
	retry := ctx.WithBlockTime(time.Unix(t0+5000+types.SweepRetrySeconds, 0))
	_, err = k.sweepExpiredRegistrations(retry, 2)
	require.NoError(t, err)
	at, err := k.SweepRetry.Get(retry, privacy.FieldBytes(privacy.U64(100)))
	require.NoError(t, err)
	require.Equal(t, t0+5000+2*types.SweepRetrySeconds, at)
}
