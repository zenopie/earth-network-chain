package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// A deposit is priced at the ratio the pool holds when it executes, so a trade
// landing between signing and execution changes what the depositor gets.
// min_shares is how they say what they will accept.
func TestAddLiquidityHonoursMinShares(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))

	provider := bech32(t, sdk.AccAddress("provider____________"))
	deposit := &types.MsgAddLiquidity{
		Creator: provider,
		PoolId:  1,
		AmountA: sdk.NewInt64Coin("uerth", 10_000),
		AmountB: sdk.NewInt64Coin("utok", 10_000),
	}

	// At a 1:1 pool, 10k of each mints 10k shares. Asking for more is refused,
	// and refused before any coins move.
	tooMuch := *deposit
	tooMuch.MinShares = "10001"
	_, err := ms.AddLiquidity(ctx, &tooMuch)
	require.ErrorIs(t, err, types.ErrSlippage)
	require.True(t, bank.sentTo(sdk.AccAddress("provider____________")).IsZero(),
		"a refused deposit must not have taken anything")

	// Exactly what it will mint is accepted.
	justRight := *deposit
	justRight.MinShares = "10000"
	res, err := ms.AddLiquidity(ctx, &justRight)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(10_000), res.Shares.Amount)

	require.NoError(t, k.AssertInvariants(ctx))
}

// Empty is no minimum: every client built before the field existed sends
// nothing, and must keep working.
func TestAddLiquidityWithoutMinSharesIsUnchanged(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))

	res, err := ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
		Creator: bech32(t, sdk.AccAddress("provider____________")),
		PoolId:  1,
		AmountA: sdk.NewInt64Coin("uerth", 10_000),
		AmountB: sdk.NewInt64Coin("utok", 10_000),
	})
	require.NoError(t, err)
	require.Equal(t, math.NewInt(10_000), res.Shares.Amount)
}

// A malformed value is rejected rather than read as zero, which would silently
// turn a slippage bound into no bound at all.
func TestAddLiquidityRejectsMalformedMinShares(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))

	for _, bad := range []string{"nonsense", "-1", "1.5"} {
		_, err := ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
			Creator:   bech32(t, sdk.AccAddress("provider____________")),
			PoolId:    1,
			AmountA:   sdk.NewInt64Coin("uerth", 10_000),
			AmountB:   sdk.NewInt64Coin("utok", 10_000),
			MinShares: bad,
		})
		require.ErrorIs(t, err, types.ErrInvalidAmount, "min_shares %q", bad)
	}
}
