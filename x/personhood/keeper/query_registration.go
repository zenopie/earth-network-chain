package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"cosmossdk.io/collections"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Registration looks a registration up by its passport nullifier.
func (q queryServer) Registration(ctx context.Context, req *types.QueryRegistrationRequest) (*types.QueryRegistrationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	nf, err := hex.DecodeString(strings.TrimPrefix(req.Nullifier, "0x"))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "nullifier must be hex")
	}
	reg, err := q.k.Registrations.Get(ctx, nf)
	if errors.Is(err, collections.ErrNotFound) {
		return &types.QueryRegistrationResponse{}, nil
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	expired, err := q.k.isExpired(ctx, reg)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRegistrationResponse{Registered: !expired, Expired: expired, Registration: reg}, nil
}

// RegistrationCount returns the registration headcount.
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

// IdentityTree returns the tree's size and latest anchor.
func (q queryServer) IdentityTree(ctx context.Context, _ *types.QueryIdentityTreeRequest) (*types.QueryIdentityTreeResponse, error) {
	size, err := q.k.IdentityTreeSize(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	latest, err := q.k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	window, err := q.k.IdentityRootWindow(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryIdentityTreeResponse{Size_: size, LatestRoot: latest, WindowSeconds: uint64(window)}, nil
}

// IdentityLeaves returns leaves [start, start+limit).
func (q queryServer) IdentityLeaves(ctx context.Context, req *types.QueryIdentityLeavesRequest) (*types.QueryIdentityLeavesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	size, err := q.k.IdentityTreeSize(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	limit := req.Limit
	if limit == 0 || limit > types.MaxIdentityLeavesQuery {
		limit = types.MaxIdentityLeavesQuery
	}
	out := &types.QueryIdentityLeavesResponse{}
	for i := req.Start; i < size && i < req.Start+limit; i++ {
		l, err := q.k.IdentityLeafAt(ctx, i)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		out.Leaves = append(out.Leaves, privacy.FieldBytes(l))
	}
	return out, nil
}

// CaretakerVoterCount is how many caretaker splits currently count.
func (q queryServer) CaretakerVoterCount(ctx context.Context, _ *types.QueryCaretakerVoterCountRequest) (*types.QueryCaretakerVoterCountResponse, error) {
	n, err := q.k.getCaretakerCount(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryCaretakerVoterCountResponse{Count: n}, nil
}
