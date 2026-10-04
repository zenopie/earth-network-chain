package keeper

import (
	"context"
	"encoding/hex"
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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
	if _, err := q.k.valAddr(req.Validator); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
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

// MaxValidatorsPage is Query/Validators' largest page.
const MaxValidatorsPage = 200

// Validators pages every validator with what quoting reads (ValidatorQuote):
// x/staking's validators through its own paged query (its keys, its
// next_key), then, on the last page, every book whose validator x/staking
// has removed.
func (q queryServer) Validators(ctx context.Context, req *types.QueryValidatorsRequest) (*types.QueryValidatorsResponse, error) {
	if q.k.stakingQ == nil {
		return nil, status.Error(codes.Unavailable, "x/staking's validator query is not wired")
	}
	page := &query.PageRequest{}
	if req != nil && req.Pagination != nil {
		p := *req.Pagination
		page = &p
	}
	if page.Limit == 0 {
		page.Limit = query.DefaultLimit
	}
	if page.Limit > MaxValidatorsPage {
		page.Limit = MaxValidatorsPage
	}
	sres, err := q.k.stakingQ.Validators(ctx, &stakingtypes.QueryValidatorsRequest{Pagination: page})
	if err != nil {
		return nil, err
	}
	res := &types.QueryValidatorsResponse{
		Validators: make([]types.ValidatorQuote, 0, len(sres.Validators)),
		Pagination: sres.Pagination,
		Height:     sdk.UnwrapSDKContext(ctx).BlockHeight(),
	}
	for _, v := range sres.Validators {
		vq, err := q.k.validatorQuote(ctx, v.OperatorAddress, v)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		res.Validators = append(res.Validators, vq)
	}
	if res.Pagination == nil {
		res.Pagination = &query.PageResponse{}
	}
	last := len(res.Pagination.NextKey) == 0
	// x/staking counts a total only for a page asked by offset.
	countTotal := page.CountTotal && len(page.Key) == 0
	if !last && !countTotal {
		return res, nil
	}
	// Books left by validators x/staking removed (a book outlives its
	// validator only while its value winds down): on the last page, and
	// in the total.
	removed, err := q.k.removedValidatorBooks(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if countTotal {
		res.Pagination.Total += uint64(len(removed))
	}
	if last {
		for _, valoper := range removed {
			vq, err := q.k.validatorQuote(ctx, valoper, stakingtypes.Validator{})
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			res.Validators = append(res.Validators, vq)
		}
	}
	return res, nil
}

// removedValidatorBooks are the books whose validator x/staking has
// removed, in key order.
func (k Keeper) removedValidatorBooks(ctx context.Context) ([]string, error) {
	var removed []string
	err := k.Validators.Walk(ctx, nil, func(valoper string, _ types.ValidatorState) (bool, error) {
		val, err := k.valAddr(valoper)
		if err != nil {
			return true, err
		}
		if _, err := k.staking.GetValidator(ctx, val); errors.Is(err, stakingtypes.ErrNoValidatorFound) {
			removed = append(removed, valoper)
		} else if err != nil {
			return true, err
		}
		return false, nil
	})
	return removed, err
}

// validatorQuote is one validator's ValidatorQuote; v is x/staking's
// validator (zero when x/staking removed it).
func (k Keeper) validatorQuote(ctx context.Context, valoper string, v stakingtypes.Validator) (types.ValidatorQuote, error) {
	val, err := k.valAddr(valoper)
	if err != nil {
		return types.ValidatorQuote{}, err
	}
	bk, err := k.book(ctx, valoper)
	if err != nil {
		return types.ValidatorQuote{}, err
	}
	vq := types.ValidatorQuote{
		Validator:  valoper,
		Staking:    v,
		Book:       bk.state,
		Backing:    bk.backing,
		Supply:     bk.supply,
		Rate:       rateOf(bk.backing, bk.supply),
		Delegation: bk.delegation,
		Rewards:    bk.rewards,
	}
	if v.OperatorAddress != "" {
		if cons, err := v.GetConsAddr(); err == nil {
			vq.Tombstoned = k.slashing.IsTombstoned(ctx, cons)
		}
	}
	refusal := k.checkDelegatable(ctx, valoper)
	if refusal == nil && !bk.supply.IsPositive() && bk.backing.IsPositive() {
		refusal = types.ErrValidator.Wrapf("%s's book is settling (no derth, %s%s backing) until the epoch end", valoper, bk.backing, types.BondDenom)
	}
	if refusal != nil {
		vq.Refusal = refusal.Error()
	} else {
		vq.Delegatable = true
	}
	reds, err := k.staking.GetRedelegationsFromSrcValidator(ctx, val)
	if err != nil {
		return types.ValidatorQuote{}, err
	}
	for _, red := range reds {
		if red.DelegatorAddress != k.modString(ctx) {
			continue
		}
		vq.Redelegations = append(vq.Redelegations, types.RedelegationLoad{
			DstValidator:   red.ValidatorDstAddress,
			Entries:        uint32(len(red.Entries)),
			CountedEntries: uint32(countedEntries(red.Entries)),
		})
	}
	return vq, nil
}

func (q queryServer) UnbondRecord(ctx context.Context, req *types.QueryUnbondRecordRequest) (*types.QueryUnbondRecordResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if _, err := q.k.valAddr(req.Validator); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	r, err := q.k.UnbondRecords.Get(ctx, collections.Join(req.Validator, req.Epoch))
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QueryUnbondRecordResponse{Record: r}, nil
}

func (q queryServer) UnbondPayout(ctx context.Context, req *types.QueryUnbondPayoutRequest) (*types.QueryUnbondPayoutResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	p, err := q.k.UnbondPayouts.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	r, err := q.k.UnbondRecords.Get(ctx, collections.Join(p.Validator, p.Epoch))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryUnbondPayoutResponse{Payout: p, Record: r}, nil
}

func (q queryServer) Position(ctx context.Context, req *types.QueryPositionRequest) (*types.QueryPositionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	p, err := q.k.Positions.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QueryPositionResponse{Position: q.k.withLiveWeight(ctx, p)}, nil
}

func (q queryServer) Positions(ctx context.Context, req *types.QueryPositionsRequest) (*types.QueryPositionsResponse, error) {
	var pr *query.PageRequest
	if req != nil {
		pr = req.Pagination
	}
	ps, page, err := query.CollectionPaginate(ctx, q.k.Positions, pr,
		func(_ uint64, p types.Position) (types.Position, error) { return q.k.withLiveWeight(ctx, p), nil })
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

func (q queryServer) StakeTree(ctx context.Context, _ *types.QueryStakeTreeRequest) (*types.QueryStakeTreeResponse, error) {
	size, root, err := q.k.StakeTreeState(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryStakeTreeResponse{Size_: size, Root: root}, nil
}

func (q queryServer) StakeNullifier(ctx context.Context, req *types.QueryStakeNullifierRequest) (*types.QueryStakeNullifierResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nullifier required")
	}
	nf, err := hex.DecodeString(req.Nullifier)
	if err != nil || len(nf) != 32 {
		return nil, status.Error(codes.InvalidArgument, "nullifier must be 32 bytes, hex")
	}
	idx, err := q.k.StakeNullifiers.Get(ctx, nf)
	if errors.Is(err, collections.ErrNotFound) {
		return &types.QueryStakeNullifierResponse{}, nil
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryStakeNullifierResponse{Spent: true, Index: idx}, nil
}

// StakeNullifierTree pages the nullifier tree's values in insertion order.
func (q queryServer) StakeNullifierTree(ctx context.Context, req *types.QueryStakeNullifierTreeRequest) (*types.QueryStakeNullifierTreeResponse, error) {
	if req == nil {
		req = &types.QueryStakeNullifierTreeRequest{}
	}
	limit := req.Limit
	if limit == 0 || limit > 1000 {
		limit = 1000
	}
	size, root, latest, err := q.k.StakeNullifierTree(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	res := &types.QueryStakeNullifierTreeResponse{Size_: size, Root: root, LatestRoot: latest}
	rng := new(collections.Range[uint64]).StartInclusive(req.Start + 1)
	err = q.k.StakeNfValues.Walk(ctx, rng, func(_ uint64, v []byte) (bool, error) {
		res.Values = append(res.Values, v)
		return uint64(len(res.Values)) >= limit, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return res, nil
}

// DebtTree pages the slash debt tree's rows, with its size, root and the
// label window.
func (q queryServer) DebtTree(ctx context.Context, req *types.QueryDebtTreeRequest) (*types.QueryDebtTreeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	limit := req.Limit
	if limit == 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := q.k.DebtRows(ctx, req.Start, limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	root, size, err := q.k.DebtRoot(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	window, err := q.k.labelWindow(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	cb, err := q.k.ClearBefore(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryDebtTreeResponse{Rows: rows, Size_: size, Root: root, WindowSeconds: window, ClearBefore: cb}, nil
}

// Move is a move still open to slashing, and its debt row if slashed.
func (q queryServer) Move(ctx context.Context, req *types.QueryMoveRequest) (*types.QueryMoveResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	key, err := hex.DecodeString(req.Key)
	if err != nil || len(key) != 32 {
		return nil, status.Error(codes.InvalidArgument, "key: 64 hex characters")
	}
	res := &types.QueryMoveResponse{}
	if mv, err := q.k.Moves.Get(ctx, key); err == nil {
		res.Move, res.Found = mv, true
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if r, err := q.k.DebtRetained.Get(ctx, key); err == nil {
		res.Slashed, res.Retained = true, r
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return res, nil
}
