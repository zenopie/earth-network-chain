package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/types"
)

// SimulateSwapExactIn prices a swap with the swap itself, at the current
// state, writing nothing (see Keeper.simulateSwapExactIn). It is what a
// wallet's local constant-product maths approximates: the chain settles each
// pool's pending LP rewards into its reserves before pricing, which local
// maths over the stored reserves cannot see.
func (q queryServer) SimulateSwapExactIn(ctx context.Context, req *types.QuerySimulateSwapExactInRequest) (*types.QuerySimulateSwapExactInResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	for _, d := range []string{req.OfferDenom, req.AskDenom} {
		if err := sdk.ValidateDenom(d); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "denom %q: %v", d, err)
		}
	}
	if req.OfferAmount.IsNil() || !req.OfferAmount.IsPositive() {
		return nil, status.Error(codes.InvalidArgument, "offer_amount must be positive")
	}
	hub, err := q.k.HubDenom(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out, fees, err := q.k.simulateSwapExactIn(ctx, sdk.NewCoin(req.OfferDenom, req.OfferAmount), req.AskDenom)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &types.QuerySimulateSwapExactInResponse{
		TokenOut:   out,
		Fee:        sdk.NewCoin(hub, fees.fee),
		ErthBurned: fees.burn,
	}, nil
}
