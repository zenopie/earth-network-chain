package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/types"
)

// LiquidityAuction returns the genesis liquidity auction's current state.
func (q queryServer) LiquidityAuction(ctx context.Context, req *types.QueryLiquidityAuctionRequest) (*types.QueryLiquidityAuctionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	a, err := q.k.getAuction(ctx)
	if err != nil {
		if errors.Is(err, types.ErrAuctionUnavailable) {
			// Not an error for a reader: a chain with no auction configured
			// should answer "there isn't one", not fail the query.
			return &types.QueryLiquidityAuctionResponse{}, nil
		}
		return nil, err
	}
	return &types.QueryLiquidityAuctionResponse{Auction: &a}, nil
}

// AuctionBid returns one bidder's contribution and what they can claim now.
func (q queryServer) AuctionBid(ctx context.Context, req *types.QueryAuctionBidRequest) (*types.QueryAuctionBidResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addrBz, err := q.k.addressCodec.StringToBytes(req.Bidder)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid bidder address")
	}

	a, err := q.k.getAuction(ctx)
	if err != nil {
		if errors.Is(err, types.ErrAuctionUnavailable) {
			return nil, status.Error(codes.NotFound, "no liquidity auction is configured")
		}
		return nil, err
	}

	bid, err := q.k.AuctionBids.Get(ctx, addrBz)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no bid for this address")
		}
		return nil, err
	}

	claimable := sdk.NewCoin(a.ErthForBidders.Denom, math.ZeroInt())
	if amt := q.k.claimableFor(a, bid); amt.IsPositive() {
		claimable = sdk.NewCoin(a.ErthForBidders.Denom, amt)
	}

	return &types.QueryAuctionBidResponse{Bid: bid, Claimable: claimable}, nil
}
