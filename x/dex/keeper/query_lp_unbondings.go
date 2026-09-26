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
// The store is keyed by (completion_time, pool_id, address), ordered for the
// end-blocker's sweep. This used to walk all of it and filter, so one query
// cost every withdrawal on the chain; it now reads the provider's own entries
// from the address index and looks each one up.
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
	rng := collections.NewPrefixedTripleRange[[]byte, int64, uint64](want.Bytes())
	err = q.k.LpUnbondingsByAddr.Walk(ctx, rng, func(key collections.Triple[[]byte, int64, uint64]) (bool, error) {
		value, err := q.k.LpUnbondings.Get(ctx, collections.Join3(key.K2(), key.K3(), key.K1()))
		if err != nil {
			return true, err
		}
		out = append(out, value)
		return false, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryLpUnbondingsResponse{Unbondings: out}, nil
}
