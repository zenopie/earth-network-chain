package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/earth/types"
)

// Burns returns everything the chain has destroyed, by mechanism and in total.
//
// An empty response means no burn has been recorded, which on a chain past its
// first block is a bug rather than a state: the gas split runs every block that
// carries a transaction. It is reported as empty regardless, because a query
// inventing a number it cannot support would be worse.
func (q queryServer) Burns(ctx context.Context, req *types.QueryBurnsRequest) (*types.QueryBurnsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	bySource, err := q.k.BurnedBySource(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	total, err := q.k.TotalBurned(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &types.QueryBurnsResponse{BySource: bySource, Total: total}, nil
}
