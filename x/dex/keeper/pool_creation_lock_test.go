package keeper_test

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// Pool creation is locked until the genesis liquidity auction settles.
//
// The auction cannot defend its own bid denom: one pool per spoke token,
// StartLiquidityAuction refuses to open when that denom already has one, and no
// pool can ever be deleted. So a dust pool created beforehand would block the
// auction permanently — and the proposal to open it publishes the denom a voting
// period in advance. The lock closes that window and lifts itself at settlement.

// createPool is the message every test here is checking the gate on.
func createPool(t *testing.T, ms types.MsgServer, ctx sdk.Context, creator, token string) error {
	t.Helper()
	_, err := ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: creator,
		AmountA: sdk.NewInt64Coin("uerth", 500_000),
		AmountB: sdk.NewInt64Coin(token, 500_000),
	})
	return err
}

func TestPoolCreationLockedWhileAuctionPending(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx) // genesis state: PENDING

	_, outsider := bidderAddr(t, 9)
	require.ErrorIs(t, createPool(t, ms, ctx, outsider, "other"), types.ErrPoolCreationLocked)

	has, err := k.HasPoolForToken(ctx, "other")
	require.NoError(t, err)
	require.False(t, has, "a rejected creation must not claim the denom")
}

func TestPoolCreationLockedWhileAuctionOpen(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority:       govAddr(t),
		BidDenom:        bidDenom,
		DurationSeconds: 3600,
	})
	require.NoError(t, err)

	// The denom the auction is about to need is the one that matters.
	_, squatter := bidderAddr(t, 9)
	require.ErrorIs(t, createPool(t, ms, ctx, squatter, bidDenom), types.ErrPoolCreationLocked)
	// ...but the lock is blanket, not denom-specific.
	require.ErrorIs(t, createPool(t, ms, ctx, squatter, "other"), types.ErrPoolCreationLocked)
}

// The lock lifts on its own — nothing has to be switched off.
func TestPoolCreationOpensAfterSettlement(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority:       govAddr(t),
		BidDenom:        bidDenom,
		DurationSeconds: 3600,
	})
	require.NoError(t, err)

	_, bidder := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: bidder, Amount: sdk.NewInt64Coin(bidDenom, 1_000),
	})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_SETTLED, a.Status)

	// Permissionless creation now works, for an ordinary account.
	_, outsider := bidderAddr(t, 9)
	require.NoError(t, createPool(t, ms, ctx, outsider, "other"))

	// And the auction's denom is protected from here on by the ordinary
	// one-pool-per-token guard rather than by the lock.
	require.ErrorIs(t, createPool(t, ms, ctx, outsider, bidDenom), types.ErrPoolExists)
}

// An expired window with no bids returns the auction to PENDING, so the lock
// stays on. Intended: the auction has to actually clear before the dex opens up.
func TestPoolCreationStaysLockedAfterNoBidExpiry(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority:       govAddr(t),
		BidDenom:        bidDenom,
		DurationSeconds: 3600,
	})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_PENDING, a.Status)

	_, outsider := bidderAddr(t, 9)
	require.ErrorIs(t, createPool(t, ms, ctx, outsider, "other"), types.ErrPoolCreationLocked)
}

// A chain that configures no auction is never locked, which is what keeps
// devnets and unit tests permissionless.
func TestPoolCreationOpenWithoutAuction(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)

	locked, err := k.PoolCreationLocked(ctx)
	require.NoError(t, err)
	require.False(t, locked)

	_, outsider := bidderAddr(t, 9)
	require.NoError(t, createPool(t, ms, ctx, outsider, "other"))
}
