package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/earth-network/earth/x/dex/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (q queryServer) ListPool(ctx context.Context, req *types.QueryAllPoolRequest) (*types.QueryAllPoolResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	idx, err := q.k.getVolumeIndex(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	pools, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.Pool,
		req.Pagination,
		func(_ uint64, value types.Pool) (types.PoolView, error) {
			return viewOf(value, idx), nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllPoolResponse{Pool: pools, Pagination: pageRes}, nil
}

// viewOf converts stored state into what a caller is allowed to see.
//
// The only real work is de-scaling the volume. VolumeWeight is swap volume
// multiplied by the chain-wide VolumeIndex at the moment each trade was
// recorded; dividing by the current index undoes that, leaving 14-day-weighted
// volume in actual uerth. The index cancels for shares either way, so nothing
// downstream needs it — which is the point of not publishing it.
//
// Read-time only: nothing here is written back. Two callers a block apart get
// different numbers for an untouched pool, and should, because the weighting is
// a function of elapsed time.
func viewOf(p types.Pool, idx math.Int) types.PoolView {
	weight := p.VolumeWeight
	if weight.IsNil() {
		weight = math.ZeroInt()
	}

	// Guard the divide rather than trusting the index. getVolumeIndex already
	// substitutes the precision floor for a missing or non-positive value, but
	// a zero here would panic in a query handler, which is the worst place on
	// the chain to panic: it is reachable by anyone, unmetered, and takes the
	// node's RPC down rather than the transaction.
	erth := math.ZeroInt()
	if idx.IsPositive() {
		erth = weight.Mul(volumeIndexPrecision).Quo(idx)
	}

	return types.PoolView{
		PoolId:        p.PoolId,
		ReserveErth:   p.ReserveErth,
		ReserveToken:  p.ReserveToken,
		VolumeErth:    erth,
		LastTradedDay: p.LastTradedDay,
	}
}

func (q queryServer) GetPool(ctx context.Context, req *types.QueryGetPoolRequest) (*types.QueryGetPoolResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, err := q.k.Pool.Get(ctx, req.PoolId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not found")
		}

		return nil, status.Error(codes.Internal, "internal error")
	}

	idx, err := q.k.getVolumeIndex(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryGetPoolResponse{Pool: viewOf(val, idx)}, nil
}
