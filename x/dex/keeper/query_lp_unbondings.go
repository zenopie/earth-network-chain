package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/types"
)

// LpUnbondings lists the withdrawals a provider has waiting to mature.
//
// A withdrawal pays out on its own when the escrow ends, so between submitting
// one and it landing there is nothing in the provider's balance to look at: the
// shares have gone and the assets have not arrived. This is how they see that
// it is coming, and when.
//
// The store is keyed by (completion_time, pool_id, address) — ordered for the
// end-blocker's sweep, which asks "what has matured" and never "whose is this".
// So this walks the map and filters rather than seeking a prefix. That is
// linear in outstanding withdrawals chain-wide, which is fine while the set is
// small and worth revisiting with a secondary index if it is not: the entries
// live only for the unbonding period, so the set is bounded by withdrawal rate
// times a week rather than growing forever.
func (q queryServer) LpUnbondings(
	ctx context.Context,
	req *types.QueryLpUnbondingsRequest,
) (*types.QueryLpUnbondingsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Compared as bytes, not as text: the same account can be written with a
	// different case or spacing and still be the same account, and the record
	// holds whatever string the sender used.
	wantBz, err := q.k.addressCodec.StringToBytes(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid address")
	}
	want := sdk.AccAddress(wantBz)

	out := make([]types.LpUnbonding, 0)
	err = q.k.LpUnbondings.Walk(ctx, nil, func(
		key collections.Triple[int64, uint64, []byte],
		value types.LpUnbonding,
	) (bool, error) {
		if want.Equals(sdk.AccAddress(key.K3())) {
			out = append(out, value)
		}
		return false, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryLpUnbondingsResponse{Unbondings: out}, nil
}
