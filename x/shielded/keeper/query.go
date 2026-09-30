package keeper

import (
	"context"
	"encoding/hex"
	"errors"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

var _ types.QueryServer = queryServer{}

// NewQueryServerImpl returns an implementation of the QueryServer interface
// for the provided Keeper.
func NewQueryServerImpl(k Keeper) types.QueryServer {
	return queryServer{k}
}

type queryServer struct {
	k Keeper
}

func parseField(name, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: %v", name, err)
	}
	if _, err := privacy.FieldFromBytes(b); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: %v", name, err)
	}
	return b, nil
}

func (q queryServer) Params(ctx context.Context, req *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	params, err := q.k.Params.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryParamsResponse{Params: params}, nil
}

func (q queryServer) Tree(ctx context.Context, req *types.QueryTreeRequest) (*types.QueryTreeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	size, err := q.k.Size(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	root, err := q.k.CurrentRoot(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &types.QueryTreeResponse{TreeSize: size, Root: hex.EncodeToString(root)}
	latest, err := q.k.LatestRoot.Get(ctx)
	if err == nil {
		if resp.Anchor, err = q.k.Roots.Get(ctx, latest); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return resp, nil
}

func (q queryServer) Roots(ctx context.Context, req *types.QueryRootsRequest) (*types.QueryRootsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	pagination := req.Pagination
	if pagination == nil {
		pagination = &query.PageRequest{}
	}
	// Newest first unless the caller asked otherwise.
	if req.Pagination == nil {
		pagination.Reverse = true
	}
	keys, pageRes, err := query.CollectionPaginate(ctx, q.k.RootsByTime, pagination,
		func(key collections.Pair[int64, []byte], _ collections.NoValue) (collections.Pair[int64, []byte], error) {
			return key, nil
		})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]types.RootRecord, 0, len(keys))
	for _, key := range keys {
		rec, err := q.k.Roots.Get(ctx, key.K2())
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		out = append(out, rec)
	}
	return &types.QueryRootsResponse{Roots: out, Pagination: pageRes}, nil
}

func (q queryServer) Root(ctx context.Context, req *types.QueryRootRequest) (*types.QueryRootResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	root, err := parseField("root", req.Root)
	if err != nil {
		return nil, err
	}
	valid, rec, expires, err := q.k.Anchor(ctx, root)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &types.QueryRootResponse{Valid: valid, ExpiresAt: expires}
	if len(rec.Root) > 0 {
		resp.Record = &rec
	}
	return resp, nil
}

func (q queryServer) Nullifier(ctx context.Context, req *types.QueryNullifierRequest) (*types.QueryNullifierResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	nf, err := parseField("nullifier", req.Nullifier)
	if err != nil {
		return nil, err
	}
	spent, err := q.k.Nullifiers.Has(ctx, nf)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryNullifierResponse{Spent: spent}, nil
}

func (q queryServer) Assets(ctx context.Context, req *types.QueryAssetsRequest) (*types.QueryAssetsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	assets, pageRes, err := query.CollectionPaginate(ctx, q.k.Assets, req.Pagination,
		func(denom string, id []byte) (types.Asset, error) {
			return types.Asset{Denom: denom, AssetId: id}, nil
		})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryAssetsResponse{Assets: assets, Pagination: pageRes}, nil
}

func (q queryServer) Turnstiles(ctx context.Context, req *types.QueryTurnstilesRequest) (*types.QueryTurnstilesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ts, pageRes, err := query.CollectionPaginate(ctx, q.k.Turnstiles, req.Pagination,
		func(_ string, t types.Turnstile) (types.Turnstile, error) {
			return t, nil
		})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryTurnstilesResponse{Turnstiles: ts, Pagination: pageRes}, nil
}
