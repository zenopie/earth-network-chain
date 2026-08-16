package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/deflation/types"
)

func (q queryServer) AllocationOption(ctx context.Context, req *types.QueryAllocationOptionRequest) (*types.QueryAllocationOptionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	opt, err := q.k.AllocationOptions.Get(ctx, req.Id)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &types.QueryAllocationOptionResponse{Option: opt}, nil
}

func (q queryServer) AllocationOptions(ctx context.Context, req *types.QueryAllocationOptionsRequest) (*types.QueryAllocationOptionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	var options []types.AllocationOption
	if err := q.k.AllocationOptions.Walk(ctx, nil, func(_ uint64, opt types.AllocationOption) (bool, error) {
		options = append(options, opt)
		return false, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rewardIndex, err := q.k.getRewardIndex(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	totalWeight, err := q.k.getTotalWeight(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryAllocationOptionsResponse{
		Options:     options,
		RewardIndex: rewardIndex,
		TotalWeight: totalWeight,
	}, nil
}

func (q queryServer) Voter(ctx context.Context, req *types.QueryVoterRequest) (*types.QueryVoterResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addrBz, err := q.k.addressCodec.StringToBytes(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	voter, err := q.k.Voters.Get(ctx, addrBz)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &types.QueryVoterResponse{Voter: voter}, nil
}
