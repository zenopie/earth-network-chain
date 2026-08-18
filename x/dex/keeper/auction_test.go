package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// The pre-mine split: two thirds of 2,522,880,000 ERTH goes to the auction, in
// two equal halves. Scaled down here so the arithmetic stays readable.
const (
	earmark  = 840_960_000
	bidDenom = "uusdc"
)

func govAddr(t *testing.T) string {
	t.Helper()
	s, err := sdk.Bech32ifyAddressBytes(
		sdk.GetConfig().GetBech32AccountAddrPrefix(),
		authtypes.NewModuleAddress(types.GovModuleName),
	)
	require.NoError(t, err)
	return s
}

// seedAuction puts the auction in the state genesis leaves it in: PENDING, with
// both earmarks recorded and nothing bid.
func seedAuction(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
	t.Helper()
	require.NoError(t, k.LiquidityAuction.Set(ctx, types.LiquidityAuction{
		Status:         types.AUCTION_STATUS_PENDING,
		ErthForBidders: sdk.NewInt64Coin("uerth", earmark),
		ErthForPool:    sdk.NewInt64Coin("uerth", earmark),
		TotalRaised:    math.ZeroInt(),
		Claimed:        math.ZeroInt(),
	}))
}

func bidderAddr(t *testing.T, b byte) (sdk.AccAddress, string) {
	t.Helper()
	addr := sdk.AccAddress{b, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	s, err := sdk.Bech32ifyAddressBytes(sdk.GetConfig().GetBech32AccountAddrPrefix(), addr)
	require.NoError(t, err)
	return addr, s
}

// The whole point of the design: because both earmarks are equal, the pool opens
// at exactly the price the auction cleared at. Bidders paid `raised` for
// `earmark` ERTH; the pool is seeded with `earmark` ERTH against the same
// `raised`. There is no gap for the first trade to take.
func TestAuctionSettlesAtClearingPrice(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority:       govAddr(t),
		BidDenom:        bidDenom,
		DurationSeconds: 3600,
	})
	require.NoError(t, err)

	_, aliceStr := bidderAddr(t, 1)
	_, bobStr := bidderAddr(t, 2)
	for _, b := range []struct {
		addr string
		amt  int64
	}{{aliceStr, 300}, {bobStr, 700}} {
		_, err := ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
			Bidder: b.addr, Amount: sdk.NewInt64Coin(bidDenom, b.amt),
		})
		require.NoError(t, err)
	}

	// Before the deadline nothing settles.
	require.NoError(t, k.SettleDueAuction(ctx))
	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_OPEN, a.Status)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err = k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_SETTLED, a.Status)

	pool, err := k.Pool.Get(ctx, a.PoolId)
	require.NoError(t, err)
	require.Equal(t, int64(earmark), pool.ReserveErth.Amount.Int64())
	require.Equal(t, int64(1000), pool.ReserveToken.Amount.Int64())
	require.Equal(t, bidDenom, pool.ReserveToken.Denom)

	// The pool's ERTH-per-bid-token equals what bidders paid: both are
	// earmark/1000.
	require.True(t, pool.ReserveErth.Amount.Quo(pool.ReserveToken.Amount).
		Equal(a.ErthForBidders.Amount.Quo(a.TotalRaised)))

	// LP shares were minted but never sent to anyone, so they sit on the module
	// account with no key to sign them away. That is what makes this permanent.
	shareDenom := types.LPShareDenom(a.PoolId)
	require.True(t, bank.GetSupply(ctx, shareDenom).Amount.IsPositive())
	require.True(t, bank.sent.AmountOf(shareDenom).IsZero())
}

func TestAuctionClaimIsProRata(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)

	alice, aliceStr := bidderAddr(t, 1)
	bob, bobStr := bidderAddr(t, 2)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: aliceStr, Amount: sdk.NewInt64Coin(bidDenom, 300)})
	require.NoError(t, err)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: bobStr, Amount: sdk.NewInt64Coin(bidDenom, 700)})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	_, err = ms.ClaimLiquidityAuction(ctx, &types.MsgClaimLiquidityAuction{Bidder: aliceStr})
	require.NoError(t, err)
	_, err = ms.ClaimLiquidityAuction(ctx, &types.MsgClaimLiquidityAuction{Bidder: bobStr})
	require.NoError(t, err)

	require.Equal(t, int64(earmark*300/1000), bank.sentTo(alice).AmountOf("uerth").Int64())
	require.Equal(t, int64(earmark*700/1000), bank.sentTo(bob).AmountOf("uerth").Int64())

	// Claiming twice fails rather than paying twice.
	_, err = ms.ClaimLiquidityAuction(ctx, &types.MsgClaimLiquidityAuction{Bidder: aliceStr})
	require.ErrorIs(t, err, types.ErrAlreadyClaimed)
}

// An auction nobody bids on must not strand two thirds of the pre-mine on the
// module account, where no message could ever move it again. It returns to
// PENDING so governance can open another window.
func TestAuctionWithNoBidsReturnsToPending(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_PENDING, a.Status)
	require.Equal(t, int64(earmark), a.ErthForBidders.Amount.Int64())
	require.Equal(t, int64(earmark), a.ErthForPool.Amount.Int64())
	require.Equal(t, "", a.BidDenom)

	// And it can be opened again.
	_, err = ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)
}

func TestAuctionRejectsNonAuthorityAndLateBids(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, notGov := bidderAddr(t, 9)
	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: notGov, BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.ErrorIs(t, err, types.ErrInvalidSigner)

	_, err = ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 0,
	})
	require.ErrorIs(t, err, types.ErrAuctionDuration)

	_, err = ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)

	// Opening twice is refused: the second call would reset total_raised and
	// silently discard every bid already taken.
	_, err = ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.ErrorIs(t, err, types.ErrAuctionState)

	_, aliceStr := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: aliceStr, Amount: sdk.NewInt64Coin("uwrong", 100)})
	require.ErrorIs(t, err, types.ErrInvalidDenom)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: aliceStr, Amount: sdk.NewInt64Coin(bidDenom, 100)})
	require.ErrorIs(t, err, types.ErrAuctionState)
}

// Bidding twice accumulates rather than replacing, and claims follow the total.
func TestAuctionBidsAccumulate(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: bidDenom, DurationSeconds: 3600,
	})
	require.NoError(t, err)

	alice, aliceStr := bidderAddr(t, 1)
	for _, amt := range []int64{100, 400} {
		resp, err := ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
			Bidder: aliceStr, Amount: sdk.NewInt64Coin(bidDenom, amt)})
		require.NoError(t, err)
		_ = resp
	}
	bid, err := k.AuctionBids.Get(ctx, alice)
	require.NoError(t, err)
	require.Equal(t, int64(500), bid.Amount.Int64())

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	// Sole bidder takes the whole earmark.
	_, err = ms.ClaimLiquidityAuction(ctx, &types.MsgClaimLiquidityAuction{Bidder: aliceStr})
	require.NoError(t, err)
	require.Equal(t, int64(earmark), bank.sentTo(alice).AmountOf("uerth").Int64())
}

// The intended denominator is ETH arriving over IBC, which means bid_denom is an
// `ibc/<64 hex>` hash rather than a plain denom. StartLiquidityAuction validates
// it, so pin that the SDK's denom rules accept that shape -- getting this wrong
// would only surface at the governance proposal, after the code was frozen.
func TestAuctionAcceptsIBCDenom(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedAuction(t, k, ctx)

	const ibcETH = "ibc/27394FB092D2ECCD56123C74F36E4C1F926001CEADA9CA97EA622B25F41E5EB2"

	_, err := ms.StartLiquidityAuction(ctx, &types.MsgStartLiquidityAuction{
		Authority: govAddr(t), BidDenom: ibcETH, DurationSeconds: 86400,
	})
	require.NoError(t, err)

	_, aliceStr := bidderAddr(t, 1)
	_, err = ms.BidLiquidityAuction(ctx, &types.MsgBidLiquidityAuction{
		Bidder: aliceStr, Amount: sdk.NewInt64Coin(ibcETH, 1_000_000)})
	require.NoError(t, err)

	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(48 * time.Hour))
	require.NoError(t, k.SettleDueAuction(ctx))

	a, err := k.LiquidityAuction.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, types.AUCTION_STATUS_SETTLED, a.Status)

	pool, err := k.Pool.Get(ctx, a.PoolId)
	require.NoError(t, err)
	require.Equal(t, ibcETH, pool.ReserveToken.Denom)
}
