package keeper_test

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/types"
)

// humanOption is the option in the human stream these tests point every vote at.
func humanOption(id uint64) collections.Pair[uint32, uint64] {
	return collections.Join(uint32(types.AllocationStream), id)
}

// humanVoter keys a voter row in the human stream.
func humanVoter(addr sdk.AccAddress) collections.Pair[uint32, []byte] {
	return collections.Join(uint32(types.AllocationStream), addr.Bytes())
}

// seedVoter records a registration at registeredAt and points its one-human vote
// entirely at the human stream's option 1, the way MsgRegister followed by
// MsgSetAllocations would.
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

	require.NoError(t, f.allocation.Options.Set(ctx, humanOption(1), allocationtypes.AllocationOption{
		Id:              1,
		Stream:          types.AllocationStream,
		Kind:            allocationtypes.ALLOCATION_KIND_ADDRESS,
		Recipient:       addrStr,
		AmountAllocated: math.ZeroInt(),
		Accumulated:     math.ZeroInt(),
		LastRewardIndex: math.ZeroInt(),
	}))
	require.NoError(t, f.allocation.Voters.Set(ctx, humanVoter(addr), allocationtypes.Voter{
		Percentages: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}},
		Weight:      math.NewInt(types.VoterWeight),
	}))
	total, err := f.allocation.TotalWeight.Get(ctx, uint32(types.AllocationStream))
	if err != nil {
		total = math.ZeroInt()
	}
	require.NoError(t, f.allocation.TotalWeight.Set(ctx, uint32(types.AllocationStream), total.AddRaw(types.VoterWeight)))

	opt, err := f.allocation.Options.Get(ctx, humanOption(1))
	require.NoError(t, err)
	opt.AmountAllocated = opt.AmountAllocated.AddRaw(types.VoterWeight)
	require.NoError(t, f.allocation.Options.Set(ctx, humanOption(1), opt))
}

// humanTotalWeight reads the human stream's total voting weight.
func humanTotalWeight(t *testing.T, f *fixture, ctx context.Context) math.Int {
	t.Helper()
	total, err := f.allocation.TotalWeight.Get(ctx, uint32(types.AllocationStream))
	require.NoError(t, err)
	return total
}

// TestExpirySweepClearsVoteWeight is the leak this sweep exists to stop. A human
// who registers, votes, and then lets their registration lapse used to keep
// their weight in the human stream's total forever, diluting everyone still
// verified while their option kept accruing ERTH no live human directed.
func TestExpirySweepClearsVoteWeight(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)

	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	registeredAt := int64(10_000)
	addr := sdk.AccAddress("lapsed-human________")
	seedVoter(t, f, sdkCtx, []byte("nullifier-1"), addr, registeredAt)

	require.Equal(t, int64(types.VoterWeight), humanTotalWeight(t, f, f.ctx).Int64(),
		"voter should be counted while valid")

	// Still inside the validity window: the sweep must leave them alone.
	liveCtx := sdkCtx.WithBlockTime(time.Unix(registeredAt+500, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(liveCtx))
	require.Equal(t, int64(types.VoterWeight), humanTotalWeight(t, f, liveCtx).Int64(),
		"valid registration must survive the sweep")

	// Past the window: weight is retired along with the registration.
	deadCtx := sdkCtx.WithBlockTime(time.Unix(registeredAt+2000, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))

	total := humanTotalWeight(t, f, deadCtx)
	require.True(t, total.IsZero(), "lapsed voter must not keep diluting live humans, got %s", total)

	_, err := f.keeper.Registrations.Get(deadCtx, []byte("nullifier-1"))
	require.ErrorIs(t, err, collections.ErrNotFound, "registration should be gone")
	_, err = f.allocation.Voters.Get(deadCtx, humanVoter(addr))
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

	const cohort = types.DefaultRegistrationSweepLimit + 25
	for i := 0; i < cohort; i++ {
		nullifier := []byte{byte(i / 256), byte(i % 256), 'n'}
		addr := sdk.AccAddress(append([]byte("human"), byte(i/256), byte(i%256), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
		seedVoter(t, f, sdkCtx, nullifier, addr, int64(10_000+i))
	}

	deadCtx := sdkCtx.WithBlockTime(time.Unix(20_000, 0).UTC())
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))

	count, err := f.keeper.RegCount.Get(deadCtx)
	require.NoError(t, err)
	require.Equal(t, uint64(cohort-types.DefaultRegistrationSweepLimit), count,
		"one block should retire at most the sweep limit")

	// The remainder is not stranded — the next block picks it up.
	require.NoError(t, f.keeper.BeginBlocker(deadCtx))
	count, err = f.keeper.RegCount.Get(deadCtx)
	require.NoError(t, err)
	require.Zero(t, count, "the backlog should drain on subsequent blocks")
}
