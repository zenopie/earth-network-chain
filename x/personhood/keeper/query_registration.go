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

// RegistrationCount returns the live registration headcount — the denominator of
// the human emission stream, since every registration carries the same weight.
func (q queryServer) RegistrationCount(ctx context.Context, req *types.QueryRegistrationCountRequest) (*types.QueryRegistrationCountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	count, err := q.k.getRegCount(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRegistrationCountResponse{Count: count}, nil
}
