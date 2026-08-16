package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/personhood/types"
)

func (q queryServer) Registration(ctx context.Context, req *types.QueryRegistrationRequest) (*types.QueryRegistrationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addrBz, err := q.k.addressCodec.StringToBytes(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	reg, ok, err := q.k.getRegistrationByAddr(ctx, sdk.AccAddress(addrBz))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !ok {
		return &types.QueryRegistrationResponse{Registered: false}, nil
	}
	expired, err := q.k.isExpired(ctx, reg)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRegistrationResponse{
		Registered:   !expired,
		Expired:      expired,
		Registration: reg,
	}, nil
}

func (q queryServer) DemocraticOptions(ctx context.Context, req *types.QueryDemocraticOptionsRequest) (*types.QueryDemocraticOptionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	var options []types.DemocraticOption
	if err := q.k.DemOptions.Walk(ctx, nil, func(_ uint64, opt types.DemocraticOption) (bool, error) {
		options = append(options, opt)
		return false, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rewardIndex, err := q.k.demRewardIndex(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	totalWeight, err := q.k.demTotalWeight(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	count, err := q.k.getRegCount(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryDemocraticOptionsResponse{
		Options:       options,
		RewardIndex:   rewardIndex,
		TotalWeight:   totalWeight,
		Registrations: count,
	}, nil
}

func (q queryServer) DemocraticVoter(ctx context.Context, req *types.QueryDemocraticVoterRequest) (*types.QueryDemocraticVoterResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addrBz, err := q.k.addressCodec.StringToBytes(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	voter, err := q.k.DemVoters.Get(ctx, addrBz)
	if err != nil {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &types.QueryDemocraticVoterResponse{Voter: voter}, nil
}
