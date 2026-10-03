package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/personhood/types"
)

func (q queryServer) Params(ctx context.Context, req *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	params, err := q.k.Params.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &types.QueryParamsResponse{Params: params}, nil
}

// LeaseBounds implements the query: the lease lengths the predecessor bounds
// are computed from, as enforced now, and the bounds at this block.
func (q queryServer) LeaseBounds(ctx context.Context, req *types.QueryLeaseBoundsRequest) (*types.QueryLeaseBoundsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	params, err := q.k.Params.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	handleLease, err := q.k.handleLeaseSeconds(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	caretakerLease, err := q.k.leaseSeconds(ctx, params)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	res := &types.QueryLeaseBoundsResponse{
		BlockTime:               now,
		ActivationMarginSeconds: types.ActivationMarginSeconds,
		HandleLeaseSeconds:      handleLease,
		HandleClaimBound:        now - handleLease - types.ActivationMarginSeconds,
		CaretakerLeaseSeconds:   caretakerLease,
		CaretakerCastBound:      now - caretakerLease - types.ActivationMarginSeconds,
	}
	if h, err := q.k.LeaseHold.Get(ctx); err == nil && caretakerLease == h.Seconds && now < h.Until {
		res.CaretakerLeaseHoldUntil = h.Until
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return res, nil
}
