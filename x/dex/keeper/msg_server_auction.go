package keeper

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// StartLiquidityAuction opens the bidding window. Governance-only.
//
// No funds move: the earmarks were credited at genesis. All this decides is the
// bid asset and the deadline, which are the two things that could not be known
// at genesis because the intended asset — IBC USDC — does not exist until IBC
// is enabled.
func (k msgServer) StartLiquidityAuction(ctx context.Context, msg *types.MsgStartLiquidityAuction) (*types.MsgStartLiquidityAuctionResponse, error) {
	authority, err := k.addressCodec.StringToBytes(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(k.GetAuthority(), authority) {
		expected, _ := k.addressCodec.BytesToString(k.GetAuthority())
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", expected, msg.Authority)
	}

	if msg.DurationSeconds <= 0 {
		return nil, types.ErrAuctionDuration
	}
	if err := sdk.ValidateDenom(msg.BidDenom); err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidDenom, err.Error())
	}

	hub, err := k.HubDenom(ctx)
	if err != nil {
		return nil, err
	}
	if msg.BidDenom == hub {
		return nil, errorsmod.Wrap(types.ErrInvalidDenom, "bid denom must not be the hub asset")
	}

	// Settlement creates a pool, and the dex allows one pool per spoke token. A
	// denom that already has one would fail at settlement — after bids are taken
	// and with no way to refund them — so it is refused here instead.
	if has, err := k.PoolByToken.Has(ctx, msg.BidDenom); err != nil {
		return nil, err
	} else if has {
		return nil, errorsmod.Wrapf(types.ErrPoolExists, "pool for %s already exists", msg.BidDenom)
	}

	a, err := k.getAuction(ctx)
	if err != nil {
		return nil, err
	}
	if a.Status != types.AUCTION_STATUS_PENDING {
		return nil, errorsmod.Wrapf(types.ErrAuctionState, "auction is %s, expected pending", a.Status)
	}

	a.Status = types.AUCTION_STATUS_OPEN
	a.BidDenom = msg.BidDenom
	a.EndTime = sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + msg.DurationSeconds
	a.TotalRaised = math.ZeroInt()
	if err := k.LiquidityAuction.Set(ctx, a); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"liquidity_auction_started",
			sdk.NewAttribute("bid_denom", a.BidDenom),
			sdk.NewAttribute("end_time", strconv.FormatInt(a.EndTime, 10)),
			sdk.NewAttribute("erth_for_bidders", a.ErthForBidders.String()),
			sdk.NewAttribute("erth_for_pool", a.ErthForPool.String()),
		),
	)

	return &types.MsgStartLiquidityAuctionResponse{EndTime: a.EndTime}, nil
}

// BidLiquidityAuction contributes to the open auction.
//
// Bids are additive and there is no withdrawal. That is what lets total_raised
// be treated as final at the deadline, and it is why the pool's opening price
// cannot be moved by anyone pulling out at the last block.
func (k msgServer) BidLiquidityAuction(ctx context.Context, msg *types.MsgBidLiquidityAuction) (*types.MsgBidLiquidityAuctionResponse, error) {
	bidderBz, err := k.addressCodec.StringToBytes(msg.Bidder)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid bidder address")
	}
	bidder := sdk.AccAddress(bidderBz)

	a, err := k.getAuction(ctx)
	if err != nil {
		return nil, err
	}
	if a.Status != types.AUCTION_STATUS_OPEN {
		return nil, errorsmod.Wrapf(types.ErrAuctionState, "auction is %s, expected open", a.Status)
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() >= a.EndTime {
		return nil, errorsmod.Wrap(types.ErrAuctionState, "bidding has closed")
	}

	if !msg.Amount.IsValid() || !msg.Amount.Amount.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "bid must be positive")
	}
	if msg.Amount.Denom != a.BidDenom {
		return nil, errorsmod.Wrapf(types.ErrInvalidDenom, "auction takes %s, got %s", a.BidDenom, msg.Amount.Denom)
	}

	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, bidder, types.ModuleName, sdk.NewCoins(msg.Amount)); err != nil {
		return nil, err
	}

	bid, err := k.AuctionBids.Get(ctx, bidderBz)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return nil, err
		}
		bid = types.AuctionBid{Bidder: msg.Bidder, Amount: math.ZeroInt()}
	}
	bid.Amount = bid.Amount.Add(msg.Amount.Amount)
	if err := k.AuctionBids.Set(ctx, bidderBz, bid); err != nil {
		return nil, err
	}

	a.TotalRaised = a.TotalRaised.Add(msg.Amount.Amount)
	if err := k.LiquidityAuction.Set(ctx, a); err != nil {
		return nil, err
	}

	total := sdk.NewCoin(a.BidDenom, bid.Amount)
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"liquidity_auction_bid",
			sdk.NewAttribute("bidder", msg.Bidder),
			sdk.NewAttribute("amount", msg.Amount.String()),
			sdk.NewAttribute("total_bid", total.String()),
			sdk.NewAttribute("total_raised", a.TotalRaised.String()),
		),
	)

	return &types.MsgBidLiquidityAuctionResponse{TotalBid: total}, nil
}

// ClaimLiquidityAuction pays a bidder their pro-rata share of the bidder
// earmark. Available only after settlement, and only once.
func (k msgServer) ClaimLiquidityAuction(ctx context.Context, msg *types.MsgClaimLiquidityAuction) (*types.MsgClaimLiquidityAuctionResponse, error) {
	bidderBz, err := k.addressCodec.StringToBytes(msg.Bidder)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid bidder address")
	}
	bidder := sdk.AccAddress(bidderBz)

	a, err := k.getAuction(ctx)
	if err != nil {
		return nil, err
	}
	if a.Status != types.AUCTION_STATUS_SETTLED {
		return nil, errorsmod.Wrapf(types.ErrAuctionState, "auction is %s, expected settled", a.Status)
	}

	bid, err := k.AuctionBids.Get(ctx, bidderBz)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, types.ErrNoBid
		}
		return nil, err
	}
	if bid.Claimed {
		return nil, types.ErrAlreadyClaimed
	}

	amt := k.claimableFor(a, bid)
	if !amt.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "claimable amount is zero")
	}
	payout := sdk.NewCoin(a.ErthForBidders.Denom, amt)

	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, bidder, sdk.NewCoins(payout)); err != nil {
		return nil, err
	}

	bid.Claimed = true
	if err := k.AuctionBids.Set(ctx, bidderBz, bid); err != nil {
		return nil, err
	}
	a.Claimed = a.Claimed.Add(amt)
	if err := k.LiquidityAuction.Set(ctx, a); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"liquidity_auction_claim",
			sdk.NewAttribute("bidder", msg.Bidder),
			sdk.NewAttribute("amount", payout.String()),
		),
	)

	return &types.MsgClaimLiquidityAuctionResponse{Amount: payout}, nil
}
