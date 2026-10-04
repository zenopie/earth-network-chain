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

// bumpCount increments a tally keyed by k, treating "not found" as zero.
func bumpCount[K any](ctx context.Context, m collections.Map[K, uint64], key K) error {
	n, err := m.Get(ctx, key)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	return m.Set(ctx, key, n+1)
}

// dropCount decrements a tally keyed by k, removing the entry when it reaches
// zero so retired signers and countries stop showing up in the explorer. Missing
// or already-zero entries are left alone rather than underflowing.
func dropCount[K any](ctx context.Context, m collections.Map[K, uint64], key K) error {
	n, err := m.Get(ctx, key)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil
		}
		return err
	}
	if n <= 1 {
		return m.Remove(ctx, key)
	}
	return m.Set(ctx, key, n-1)
}

// RegistrationsByDsc returns how many humans registered with a given Document
// Signer. dsc_key is the hex-encoded Poseidon2 commitment recorded on each
// registration, so a revoked signer's blast radius is a single query.
func (q queryServer) RegistrationsByDsc(ctx context.Context, req *types.QueryRegistrationsByDscRequest) (*types.QueryRegistrationsByDscResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	key, err := hex.DecodeString(strings.TrimPrefix(req.DscKey, "0x"))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "dsc_key must be hex")
	}
	n, err := q.k.RegCountByDsc.Get(ctx, key)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryRegistrationsByDscResponse{Count: 0}, nil
		}
		return nil, err
	}
	return &types.QueryRegistrationsByDscResponse{Count: n}, nil
}

// RegistrationCountries returns registrations per issuing country, which is what
// the explorer's map renders.
func (q queryServer) RegistrationCountries(ctx context.Context, _ *types.QueryRegistrationCountriesRequest) (*types.QueryRegistrationCountriesResponse, error) {
	var out []types.CountryCount
	err := q.k.RegCountByCountry.Walk(ctx, nil, func(country string, n uint64) (bool, error) {
		out = append(out, types.CountryCount{Country: country, Count: n})
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return &types.QueryRegistrationCountriesResponse{Countries: out}, nil
}
