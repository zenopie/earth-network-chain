package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
)

// seedVoter records a registration at registeredAt and points its one-human vote
// entirely at option 1, the way MsgRegister + MsgSetDemocraticAllocations would.
func seedVoter(t *testing.T, f *fixture, ctx sdk.Context, nullifier []byte, addr sdk.AccAddress, registeredAt int64) {
	t.Helper()
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)

	require.NoError(t, f.keeper.Registrations.Set(ctx, nullifier, types.Registration{
		Nullifier:    nullifier,
		Address:      addrStr,
		RegisteredAt: registeredAt,
		DscKey:       []byte("dsc"),
		Country:      "NZ",
	}))
	require.NoError(t, f.keeper.RegByAddr.Set(ctx, addr.Bytes(), nullifier))
	require.NoError(t, f.keeper.RegByRegisteredAt.Set(ctx, collections.Join(registeredAt, nullifier)))

	count, _ := f.keeper.RegCount.Get(ctx)
	require.NoError(t, f.keeper.RegCount.Set(ctx, count+1))

	require.NoError(t, f.keeper.DemOptions.Set(ctx, 1, types.DemocraticOption{
		Id:              1,
		Kind:            types.DEMOCRATIC_KIND_ADDRESS,
		Recipient:       addrStr,
		AmountAllocated: math.ZeroInt(),
		Accumulated:     math.ZeroInt(),
		LastRewardIndex: math.ZeroInt(),
	}))
	require.NoError(t, f.keeper.DemVoters.Set(ctx, addr.Bytes(), types.DemocraticVoter{
		Percentages: []types.DemocraticWeight{{OptionId: 1, Percent: 100}},
	}))
	total, err := f.keeper.DemTotalWeight.Get(ctx)
	if err != nil {
		total = math.ZeroInt()
	}
	require.NoError(t, f.keeper.DemTotalWeight.Set(ctx, total.AddRaw(types.VoterWeight)))

	opt, err := f.keeper.DemOptions.Get(ctx, 1)
	require.NoError(t, err)
	opt.AmountAllocated = opt.AmountAllocated.AddRaw(types.VoterWeight)
	require.NoError(t, f.keeper.DemOptions.Set(ctx, 1, opt))
}

// TestExpirySweepClearsDemocraticWeight is the leak this sweep exists to stop.
// A human who registers, votes, and then lets their registration lapse used to
// keep their weight in DemTotalWeight forever, diluting everyone still verified
// while their option kept accruing ERTH no live human directed.
func TestExpirySweepClearsDemocraticWeight(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)

	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	registeredAt := int64(10_000)
	addr := sdk.AccAddress("lapsed-human________")
	seedVoter(t, f, sdkCtx, []byte("nullifier-1"), addr, registeredAt)

	total, err := f.keeper.DemTotalWeight.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(types.VoterWeight), total.Int64(), "voter should be counted while valid")

	// Still inside the validity window: the sweep must leave them alone.
	liveCtx := sdkCtx.WithBlockTime(time.Unix(registeredAt+500, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(liveCtx))
	total, err = f.keeper.DemTotalWeight.Get(liveCtx)
	require.NoError(t, err)
	require.Equal(t, int64(types.VoterWeight), total.Int64(), "valid registration must survive the sweep")

	// Past the window: weight is retired along with the registration.
	deadCtx := sdkCtx.WithBlockTime(time.Unix(registeredAt+2000, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))

	total, err = f.keeper.DemTotalWeight.Get(deadCtx)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "lapsed voter must not keep diluting live humans, got %s", total)

	_, err = f.keeper.Registrations.Get(deadCtx, []byte("nullifier-1"))
	require.ErrorIs(t, err, collections.ErrNotFound, "registration should be gone")
	_, err = f.keeper.DemVoters.Get(deadCtx, addr.Bytes())
	require.ErrorIs(t, err, collections.ErrNotFound, "voter entry should be gone")

	count, err := f.keeper.RegCount.Get(deadCtx)
	require.NoError(t, err)
	require.Zero(t, count, "headcount should drop with the registration")

	// The per-signer and per-country tallies track live registrations, so they
	// come back down too rather than counting lifetime registrations.
	_, err = f.keeper.RegCountByDsc.Get(deadCtx, []byte("dsc"))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = f.keeper.RegCountByCountry.Get(deadCtx, "NZ")
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestExpirySweepIsBounded keeps a mass-expiry event from landing unbounded work
// on one block: a cohort that all registered together retires in batches.
func TestExpirySweepIsBounded(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)

	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	const cohort = types.DefaultExpirySweepLimit + 25
	for i := 0; i < cohort; i++ {
		nullifier := []byte{byte(i / 256), byte(i % 256), 'n'}
		addr := sdk.AccAddress(append([]byte("human"), byte(i/256), byte(i%256), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
		seedVoter(t, f, sdkCtx, nullifier, addr, int64(10_000+i))
	}

	deadCtx := sdkCtx.WithBlockTime(time.Unix(20_000, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))

	count, err := f.keeper.RegCount.Get(deadCtx)
	require.NoError(t, err)
	require.Equal(t, uint64(cohort-types.DefaultExpirySweepLimit), count,
		"one block should retire at most the sweep limit")

	// The remainder is not stranded — the next block picks it up.
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))
	count, err = f.keeper.RegCount.Get(deadCtx)
	require.NoError(t, err)
	require.Zero(t, count, "the backlog should drain on subsequent blocks")
}
