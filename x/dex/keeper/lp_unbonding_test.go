package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// unbondFixture seeds a pool and gives addr LP shares in it, the way
// AddLiquidity would, so a withdrawal has something to withdraw.
func unbondFixture(t *testing.T, shares int64) (keeper.Keeper, sdk.Context, *mintingBank, sdk.AccAddress, string) {
	t.Helper()
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	addr := sdk.AccAddress("provider____________")
	addrStr, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).
		BytesToString(addr)
	require.NoError(t, err)

	bank.setSupply(types.LPShareDenom(1), math.NewInt(shares))
	return k, ctx, bank, addr, addrStr
}

// TestRemoveLiquidityDoesNotPayOutImmediately is the whole point of unbonding:
// announcing a withdrawal must not move any assets, or the pool loses the depth
// the waiting period exists to keep.
func TestRemoveLiquidityDoesNotPayOutImmediately(t *testing.T) {
	k, ctx, _, _, addrStr := unbondFixture(t, 1_000)
	ms := keeper.NewMsgServerImpl(k)

	before, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)

	resp, err := ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
		Creator: addrStr,
		PoolId:  1,
		Shares:  sdk.NewInt64Coin(types.LPShareDenom(1), 400),
	})
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Unix()+types.DefaultLpUnbondingSeconds, resp.CompletionTime)

	after, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, before.ReserveErth.Amount, after.ReserveErth.Amount,
		"reserves must not move when unbonding begins")
	require.Equal(t, before.ReserveToken.Amount, after.ReserveToken.Amount)
}

// TestUnbondingSweepPaysOutAtMaturity walks the full path: nothing before the
// period elapses, then an automatic sweep straight to the wallet with no claim.
func TestUnbondingSweepPaysOutAtMaturity(t *testing.T) {
	k, ctx, bank, addr, addrStr := unbondFixture(t, 1_000)
	ms := keeper.NewMsgServerImpl(k)

	_, err := ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
		Creator: addrStr,
		PoolId:  1,
		Shares:  sdk.NewInt64Coin(types.LPShareDenom(1), 400),
	})
	require.NoError(t, err)

	// One second short of maturity: still nothing.
	early := ctx.WithBlockTime(ctx.BlockTime().Add(types.DefaultLpUnbondingSeconds*time.Second - time.Second))
	require.NoError(t, k.SweepMaturedUnbondings(early))
	require.True(t, bank.sent.IsZero(), "must not pay out before the period elapses")

	due := ctx.WithBlockTime(ctx.BlockTime().Add(types.DefaultLpUnbondingSeconds * time.Second))
	require.NoError(t, k.SweepMaturedUnbondings(due))

	// 400 of 1000 shares against a 1,000,000/1,000,000 pool.
	require.Equal(t, math.NewInt(400_000), bank.sentTo(addr).AmountOf("uerth"))
	require.Equal(t, math.NewInt(400_000), bank.sentTo(addr).AmountOf("utok"))

	pool, err := k.Pool.Get(due, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(600_000), pool.ReserveErth.Amount, "reserves should drop by the payout")
	require.Equal(t, math.NewInt(600_000), pool.ReserveToken.Amount)

	// The entry is gone, so a later block cannot pay it a second time.
	empty := true
	require.NoError(t, k.LpUnbondings.Walk(due, nil,
		func(collections.Triple[int64, uint64, []byte], types.LpUnbonding) (bool, error) {
			empty = false
			return true, nil
		}))
	require.True(t, empty, "a settled unbonding must be removed from the queue")
}

// TestUnbondingKeepsEarningRewards pins the design decision: the liquidity is
// still in the pool doing the work, so it is still paid for it. The provider
// only loses the time value of locked capital.
//
// The period is shortened to an hour on purpose. At the default 7 days the
// unbonding spans a whole VolumeWindowDays window, so a pool with no trading
// decays to zero volume and earns nothing at all — which would test the decay
// rather than the thing this is about.
func TestUnbondingKeepsEarningRewards(t *testing.T) {
	k, ctx, bank, addr, addrStr := unbondFixture(t, 1_000)
	ms := keeper.NewMsgServerImpl(k)

	params := types.DefaultParams()
	params.LpUnbondingSeconds = 3600
	require.NoError(t, k.Params.Set(ctx, params))

	_, err := ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
		Creator: addrStr,
		PoolId:  1,
		Shares:  sdk.NewInt64Coin(types.LPShareDenom(1), 400),
	})
	require.NoError(t, err)

	// A reward lands while the withdrawal is in flight.
	distributeLP(t, k, ctx, bank, math.NewInt(100_000))

	due := ctx.WithBlockTime(ctx.BlockTime().Add(3600 * time.Second))
	require.NoError(t, k.SweepMaturedUnbondings(due))

	// The reward compounds into the ERTH reserve before the payout is priced, so
	// 40% of the position collects 40% of it: 400,000 + 40,000.
	require.Equal(t, math.NewInt(440_000), bank.sentTo(addr).AmountOf("uerth"),
		"an unbonding position keeps earning while its liquidity is still in the pool")
}

// TestUnbondingSweepIsBounded keeps a cohort that all unbonded together from
// landing unbounded work on one block.
func TestUnbondingSweepIsBounded(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000_000, 1_000_000_000, 1_000)

	const cohort = types.LpUnbondSweepLimit + 10
	bank.setSupply(types.LPShareDenom(1), math.NewInt(cohort*1_000))
	completion := ctx.BlockTime().Unix() + types.DefaultLpUnbondingSeconds
	for i := 0; i < cohort; i++ {
		addr := sdk.AccAddress(append([]byte("lp"), byte(i/256), byte(i%256),
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
		addrStr, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).
			BytesToString(addr)
		require.NoError(t, err)
		require.NoError(t, k.LpUnbondings.Set(ctx,
			collections.Join3(completion, uint64(1), addr.Bytes()),
			types.LpUnbonding{
				Address:        addrStr,
				PoolId:         1,
				Shares:         sdk.NewInt64Coin(types.LPShareDenom(1), 1_000),
				CompletionTime: completion,
			}))
	}

	due := ctx.WithBlockTime(time.Unix(completion, 0).UTC())
	require.NoError(t, k.SweepMaturedUnbondings(due))
	require.Equal(t, cohort-types.LpUnbondSweepLimit, countUnbondings(t, k, due),
		"one block should settle at most the sweep limit")

	// The remainder is not stranded — the next block drains it.
	require.NoError(t, k.SweepMaturedUnbondings(due))
	require.Zero(t, countUnbondings(t, k, due), "the backlog should drain on later blocks")
}

func countUnbondings(t *testing.T, k keeper.Keeper, ctx sdk.Context) int {
	t.Helper()
	n := 0
	require.NoError(t, k.LpUnbondings.Walk(ctx, nil,
		func(collections.Triple[int64, uint64, []byte], types.LpUnbonding) (bool, error) {
			n++
			return false, nil
		}))
	return n
}
