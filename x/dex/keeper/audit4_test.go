package keeper_test

import (
	"math/big"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

func pow2(n uint) math.Int { return math.NewIntFromBigInt(new(big.Int).Lsh(big.NewInt(1), n)) }

// Audit 4 PoC 1, first half: a pool seeded with a huge-supply IBC voucher
// (ICS20 amounts go to 2^256) is refused at creation by the pool cap, as is
// a swap input past it.
func TestAudit4HugeVoucherPoolRefused(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	creator := sdk.AccAddress("attacker____________")

	_, err := ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: bech32(t, creator),
		AmountA: sdk.NewInt64Coin("uerth", 1),
		AmountB: sdk.NewCoin("ibc/HUGE", pow2(200)),
	})
	require.ErrorIs(t, err, types.ErrPoolCap)

	// At the cap is fine.
	_, err = ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: bech32(t, creator),
		AmountA: sdk.NewInt64Coin("uerth", 1),
		AmountB: sdk.NewCoin("ibc/HUGE", types.MaxPoolAmount),
	})
	require.NoError(t, err)

	seedFundedPool(t, k, ctx, bank, 7, 1_000_000, 1_000_000, 0)
	_, err = k.SwapExactIn(ctx, creator, sdk.NewCoin(seedTokenDenom(7), pow2(200)), "uerth", math.ZeroInt())
	require.ErrorIs(t, err, types.ErrPoolCap)
}

// Audit 4 PoC 1, second half: state past the cap (as it could have been
// before the cap existed) no longer panics the EndBlocker's withdrawal
// payout: the arithmetic is big.Int, and an entry that cannot settle is
// dropped instead of halting the chain.
func TestAudit4UnbondingPayoutOverflowNoHalt(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	creator := sdk.AccAddress("attacker____________")

	cr, err := ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: bech32(t, creator),
		AmountA: sdk.NewInt64Coin("uerth", 1),
		AmountB: sdk.NewCoin("ibc/HUGE", pow2(100)),
	})
	require.NoError(t, err)
	id := cr.PoolId
	_, err = ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
		Creator: bech32(t, creator),
		PoolId:  id,
		Shares:  sdk.NewCoin(types.LPShareDenom(id), pow2(50)),
	})
	require.NoError(t, err)

	// Force pre-cap state: shares*reserve = 2^50 * 2^250 overflows 256 bits.
	pool, err := k.Pool.Get(ctx, id)
	require.NoError(t, err)
	pool.ReserveToken.Amount = pow2(250)
	require.NoError(t, k.Pool.Set(ctx, id, pool))
	bank.setSupply(types.LPShareDenom(id), pow2(200))

	params, _ := k.Params.Get(ctx)
	later := ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(params.LpUnbondingSeconds+1) * time.Second)).
		WithEventManager(sdk.NewEventManager())
	require.NotPanics(t, func() { require.NoError(t, k.SweepMaturedUnbondings(later)) })

	n := 0
	require.NoError(t, k.LpUnbondings.Walk(later, nil, func(_ collections.Triple[int64, uint64, []byte], _ types.LpUnbonding) (bool, error) {
		n++
		return false, nil
	}))
	require.Zero(t, n, "the entry is dropped, not retried every block")
	failed := false
	for _, e := range later.EventManager().Events() {
		failed = failed || e.Type == "lp_unbond_payout_failed"
	}
	require.True(t, failed)
}

// Audit 4 PoC 2 (C2): each pulled deposit leg is rounded UP, so a depositor
// cannot be minted shares worth nearly twice the token leg they paid.
func TestAudit4DepositLegsRoundedUp(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000_000_000, 1, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))

	res, err := ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
		Creator: bech32(t, sdk.AccAddress("provider____________")),
		PoolId:  1,
		AmountA: sdk.NewInt64Coin("uerth", 1_999_999_000_000),
		AmountB: sdk.NewInt64Coin("utok", 2),
	})
	require.NoError(t, err)
	after, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1_999_999), res.Shares.Amount.Int64())
	require.Equal(t, int64(3), after.ReserveToken.Amount.Int64(), "2 utok pulled for ~2 utok of shares")
	require.Equal(t, int64(2_999_999_000_000), after.ReserveErth.Amount.Int64())
}

// Audit 4 PoC 3 (C4): a pool drained to 0/0 exports a genesis its own
// Validate accepts, and the export imports.
func TestAudit4DrainedPoolExportValidates(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	creator := sdk.AccAddress("attacker____________")
	cr, err := ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: bech32(t, creator),
		AmountA: sdk.NewInt64Coin("uerth", 1),
		AmountB: sdk.NewInt64Coin("ujunk", 1),
	})
	require.NoError(t, err)
	_, err = ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{
		Creator: bech32(t, creator), PoolId: cr.PoolId, Shares: sdk.NewInt64Coin(types.LPShareDenom(cr.PoolId), 1),
	})
	require.NoError(t, err)
	params, _ := k.Params.Get(ctx)
	later := ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(params.LpUnbondingSeconds+1) * time.Second))
	require.NoError(t, k.SweepMaturedUnbondings(later))
	gs, err := k.ExportGenesis(later)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	p := gs.PoolMap[0]
	require.True(t, p.ReserveErth.Amount.IsZero() && p.ReserveToken.Amount.IsZero())

	k2, ctx2, _ := initRewardFixture(t)
	require.NoError(t, k2.InitGenesis(ctx2, *gs))
}
