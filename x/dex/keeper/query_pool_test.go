package keeper_test

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

func createNPool(keeper keeper.Keeper, ctx context.Context, n int) []types.Pool {
	items := make([]types.Pool, n)
	for i := range items {
		items[i].PoolId = uint64(i)
		items[i].ReserveErth = sdk.NewInt64Coin(`uerth`, int64(i+100))
		items[i].ReserveToken = sdk.NewInt64Coin(`token`, int64(i+100))
		items[i].VolumeWeight = math.ZeroInt()
		_ = keeper.SetPool(ctx, items[i].PoolId, items[i])
	}
	return items
}

// viewOfTest mirrors what the querier returns for a pool created above.
//
// Those pools carry zero volume, so the de-scaling is a no-op and this is just
// a field-for-field copy. Written out rather than reusing the keeper's own
// viewOf so the test does not pass by sharing the bug it is checking for.
func viewOfTest(p types.Pool) types.PoolView {
	return types.PoolView{
		PoolId:        p.PoolId,
		ReserveErth:   p.ReserveErth,
		ReserveToken:  p.ReserveToken,
		VolumeErth:    p.VolumeWeight,
		LastTradedDay: p.LastTradedDay,
	}
}

func TestPoolQuerySingle(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNPool(f.keeper, f.ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetPoolRequest
		response *types.QueryGetPoolResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetPoolRequest{
				PoolId: msgs[0].PoolId,
			},
			response: &types.QueryGetPoolResponse{Pool: viewOfTest(msgs[0])},
		},
		{
			desc: "Second",
			request: &types.QueryGetPoolRequest{
				PoolId: msgs[1].PoolId,
			},
			response: &types.QueryGetPoolResponse{Pool: viewOfTest(msgs[1])},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetPoolRequest{
				PoolId: 100000,
			},
			err: status.Error(codes.NotFound, "not found"),
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := qs.GetPool(f.ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.EqualExportedValues(t, tc.response, response)
			}
		})
	}
}

func TestPoolQueryPaginated(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNPool(f.keeper, f.ctx, 5)
	// ListPool returns PoolView, not the stored Pool, so the expectations have
	// to be in the same shape.
	views := make([]types.PoolView, len(msgs))
	for i, m := range msgs {
		views[i] = viewOfTest(m)
	}

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllPoolRequest {
		return &types.QueryAllPoolRequest{
			Pagination: &query.PageRequest{
				Key:        next,
				Offset:     offset,
				Limit:      limit,
				CountTotal: total,
			},
		}
	}
	t.Run("ByOffset", func(t *testing.T) {
		step := 2
		for i := 0; i < len(msgs); i += step {
			resp, err := qs.ListPool(f.ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Pool), step)
			require.Subset(t, views, resp.Pool)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := qs.ListPool(f.ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Pool), step)
			require.Subset(t, views, resp.Pool)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := qs.ListPool(f.ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.EqualExportedValues(t, views, resp.Pool)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := qs.ListPool(f.ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
