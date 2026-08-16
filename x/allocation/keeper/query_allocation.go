package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/allocation/types"
)

func (q queryServer) Option(ctx context.Context, req *types.QueryOptionRequest) (*types.QueryOptionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if err := ValidateStream(req.Stream); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	opt, err := q.k.Options.Get(ctx, optionKey(req.Stream, req.Id))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &types.QueryOptionResponse{Option: opt}, nil
}

func (q queryServer) Options(ctx context.Context, req *types.QueryOptionsRequest) (*types.QueryOptionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if err := ValidateStream(req.Stream); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var options []types.AllocationOption
	rng := collections.NewPrefixedPairRange[uint32, uint64](key(req.Stream))
	if err := q.k.Options.Walk(ctx, rng, func(_ collections.Pair[uint32, uint64], opt types.AllocationOption) (bool, error) {
		options = append(options, opt)
		return false, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rewardIndex, err := q.k.getRewardIndex(ctx, req.Stream)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	totalWeight, err := q.k.getTotalWeight(ctx, req.Stream)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	epoch, err := q.k.getEpoch(ctx, req.Stream)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryOptionsResponse{
		Options:     options,
		RewardIndex: rewardIndex,
		TotalWeight: totalWeight,
		Epoch:       epoch,
	}, nil
}

func (q queryServer) Voter(ctx context.Context, req *types.QueryVoterRequest) (*types.QueryVoterResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if err := ValidateStream(req.Stream); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	addrBz, err := q.k.addressCodec.StringToBytes(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	voter, err := q.k.Voters.Get(ctx, voterKey(req.Stream, addrBz))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &types.QueryVoterResponse{Voter: voter}, nil
}
