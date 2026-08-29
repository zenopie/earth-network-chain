package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// An LP share denom may not be a pool's spoke asset, and the reason is the
// solvency check rather than anything about the AMM.
//
// checkPoolTokenSolvency compares one pool's spoke reserve against the module's
// whole balance of that denom, which is exact only because one pool per token
// means no other claim exists on it. LP shares break that: the module also
// holds them as the protocol's own position and as escrow against withdrawals
// in flight, and CheckBalances deliberately skips LP denoms on the held side
// for exactly that reason. A pool claiming an LP denom as its reserve turns
// every one of those coins into a surplus the module cannot account for, and a
// surplus out of the EndBlocker halts the chain.
//
// Anyone can hold a dust amount of any pool's shares, so without the guard this
// is a halt available to anybody, from an ordinary message.
func TestCreatePoolRejectsAnLpShareDenom(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)

	// Pool 1 with shares outstanding, some held by the module — the state
	// genesis leaves for the protocol-owned ANML/ERTH position.
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))
	bank.fundModule(sdk.NewInt64Coin(types.LPShareDenom(1), 400_000))

	require.NoError(t, k.AssertHotInvariants(ctx))

	attacker := sdk.AccAddress("attacker____________")
	_, err := ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: bech32(t, attacker),
		AmountA: sdk.NewInt64Coin("uerth", 1_000),
		AmountB: sdk.NewInt64Coin(types.LPShareDenom(1), 1_000),
	})
	require.ErrorIs(t, err, types.ErrLpShareDenom)

	// The point of the guard: the EndBlocker still passes.
	require.NoError(t, k.AssertHotInvariants(ctx),
		"a refused pool must leave the module accounting for exactly what it holds")
}

// The auction reaches the same state by another road: settlement makes the bid
// denom a pool's spoke reserve, so it is refused at the same point.
func TestStartLiquidityAuctionRejectsAnLpShareBidDenom(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority:       govAddr(t),
		BidDenom:        types.LPShareDenom(1),
		DurationSeconds: 3600,
	})
	require.ErrorIs(t, err, types.ErrLpShareDenom)
}

// The backstop. Every pool write goes through SetPool, so a path added later
// that forgets the boundary check still cannot land one in state.
func TestSetPoolRefusesAnLpShareReserve(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)

	err := k.SetPool(ctx, 7, types.Pool{
		PoolId:       7,
		ReserveErth:  sdk.NewInt64Coin("uerth", 1_000),
		ReserveToken: sdk.NewInt64Coin(types.LPShareDenom(1), 1_000),
		VolumeWeight: math.ZeroInt(),
	})
	require.ErrorIs(t, err, types.ErrLpShareDenom)

	has, err := k.Pool.Has(ctx, 7)
	require.NoError(t, err)
	require.False(t, has, "the refused write must not have landed")
}
