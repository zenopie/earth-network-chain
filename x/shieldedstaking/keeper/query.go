package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

type queryServer struct{ k Keeper }

// NewQueryServerImpl returns the Query service.
func NewQueryServerImpl(k Keeper) types.QueryServer { return queryServer{k: k} }

var _ types.QueryServer = queryServer{}

func (q queryServer) Params(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	p, err := q.k.Params.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryParamsResponse{Params: p}, nil
}

func (q queryServer) Epoch(ctx context.Context, _ *types.QueryEpochRequest) (*types.QueryEpochResponse, error) {
	e, err := q.k.Epoch.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryEpochResponse{Epoch: e}, nil
}

func (q queryServer) Validator(ctx context.Context, req *types.QueryValidatorRequest) (*types.QueryValidatorResponse, error) {
	if req == nil || req.Validator == "" {
		return nil, status.Error(codes.InvalidArgument, "validator required")
	}
	vs, err := q.k.ValidatorState(ctx, req.Validator)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	b, s, err := q.k.Backing(ctx, req.Validator)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &types.QueryValidatorResponse{State: vs, Rate: rateOf(b, s), Supply: s, Backing: b}, nil
}

func (q queryServer) UnbondRecord(ctx context.Context, req *types.QueryUnbondRecordRequest) (*types.QueryUnbondRecordResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	r, err := q.k.UnbondRecords.Get(ctx, collections.Join(req.Validator, req.Epoch))
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QueryUnbondRecordResponse{Record: r}, nil
}

func (q queryServer) Position(ctx context.Context, req *types.QueryPositionRequest) (*types.QueryPositionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	p, err := q.k.Positions.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QueryPositionResponse{Position: p}, nil
}

func (q queryServer) Positions(ctx context.Context, req *types.QueryPositionsRequest) (*types.QueryPositionsResponse, error) {
	var pr *query.PageRequest
	if req != nil {
		pr = req.Pagination
	}
	ps, page, err := query.CollectionPaginate(ctx, q.k.Positions, pr,
		func(_ uint64, p types.Position) (types.Position, error) { return p, nil })
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryPositionsResponse{Positions: ps, Pagination: page}, nil
}

func (q queryServer) Snapshot(ctx context.Context, req *types.QuerySnapshotRequest) (*types.QuerySnapshotResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	s, err := q.k.Snapshots.Get(ctx, req.ProposalId)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QuerySnapshotResponse{Snapshot: s}, nil
}
