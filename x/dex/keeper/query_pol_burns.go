package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/types"
)

// PolBurns returns every live protocol-owned-liquidity retirement schedule.
// Finished schedules are deleted, so an empty list is the honest answer that
// the protocol no longer owns liquidity it is still retiring.
func (q queryServer) PolBurns(ctx context.Context, req *types.QueryPolBurnsRequest) (*types.QueryPolBurnsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	resp := &types.QueryPolBurnsResponse{}
	if err := q.k.PolBurns.Walk(ctx, nil, func(_ uint64, b types.PolBurn) (stop bool, err error) {
		resp.PolBurns = append(resp.PolBurns, b)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return resp, nil
}
