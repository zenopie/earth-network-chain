package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

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

func (q queryServer) GroundworksVote(ctx context.Context, req *types.QueryGroundworksVoteRequest) (*types.QueryGroundworksVoteResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	v, err := q.k.GwVotes.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &types.QueryGroundworksVoteResponse{Vote: q.k.withLiveWeight(ctx, v)}, nil
}

func (q queryServer) GroundworksVotes(ctx context.Context, req *types.QueryGroundworksVotesRequest) (*types.QueryGroundworksVotesResponse, error) {
	var pr *query.PageRequest
	if req != nil {
		pr = req.Pagination
	}
	vs, page, err := query.CollectionPaginate(ctx, q.k.GwVotes, pr,
		func(_ uint64, v types.GroundworksVote) (types.GroundworksVote, error) {
			return q.k.withLiveWeight(ctx, v), nil
		})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryGroundworksVotesResponse{Votes: vs, Pagination: page}, nil
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

// MaxVintagesPage is Query/Vintages' largest page of retired vintages.
const MaxVintagesPage = 100

// Vintages is one validator's stake vintages: the current and base
// vintage, the live ones holding supply (with denom and retirement time),
// and one page of its retired vintages not yet fully withdrawn, each with
// its held payout's record.
func (q queryServer) Vintages(ctx context.Context, req *types.QueryVintagesRequest) (*types.QueryVintagesResponse, error) {
	if req == nil || req.Validator == "" {
		return nil, status.Error(codes.InvalidArgument, "validator required")
	}
	if _, err := q.k.valAddr(req.Validator); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	vc, err := q.k.vintageCtx(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	res := &types.QueryVintagesResponse{
		CurrentVintage: vc.Current, BaseVintage: vc.Base,
		NextRetirement: types.VintageStart(types.AddMonths(vc.Current, 1)).Unix(),
	}
	// At most 13 live vintages, and as many retiring as months since the
	// book's last epoch end (one, but for a halt).
	rng := collections.NewPrefixedPairRange[string, uint32](req.Validator)
	if err := q.k.VintageSupplies.Walk(ctx, rng, func(key collections.Pair[string, uint32], s math.Int) (bool, error) {
		m := key.K2()
		lv := types.LiveVintage{Vintage: m, Denom: types.VintageDenom(req.Validator, m, vc.Base),
			Supply: s, RetiresAt: types.VintageRetiresAt(m).Unix()}
		switch {
		case types.VintageLive(m, vc.Current):
			res.Live = append(res.Live, lv)
		case types.VintageRetired(m, vc.Current):
			c, err := q.pendingCompletion(ctx)
			if err != nil {
				return true, err
			}
			lv.ExpectedCompletion = c
			res.Retiring = append(res.Retiring, lv)
		default:
			return true, nil // not begun: none is ever stored
		}
		return false, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	page := &query.PageRequest{}
	if req.Pagination != nil {
		p := *req.Pagination
		page = &p
	}
	if page.Limit == 0 || page.Limit > MaxVintagesPage {
		page.Limit = MaxVintagesPage
	}
	retired, pr, err := query.CollectionPaginate(ctx, q.k.RetiredVintages, page,
		func(_ collections.Pair[string, uint32], rv types.RetiredVintage) (types.RetiredVintageState, error) {
			out := types.RetiredVintageState{Vintage: rv, Denom: types.VintageDenom(rv.Validator, rv.Vintage, vc.Base)}
			if rv.RemainingValue.IsPositive() {
				r, err := q.k.UnbondRecords.Get(ctx, collections.Join(rv.Validator, rv.Epoch))
				if err != nil {
					return out, err
				}
				out.Record = &r
			}
			var err error
			out.ExpectedCompletion, err = q.heldCompletion(ctx, out.Record)
			return out, err
		}, query.WithCollectionPaginationPairPrefix[string, uint32](req.Validator))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	res.Retired, res.Pagination = retired, pr
	return res, nil
}

// pendingCompletion is when a value the next epoch end undelegates would
// complete: that epoch's end plus x/staking's unbonding_time (unix
// seconds; an estimate, later if max_entries defers it).
func (q queryServer) pendingCompletion(ctx context.Context) (int64, error) {
	epoch, err := q.k.Epoch.Get(ctx)
	if err != nil {
		return 0, err
	}
	ut, err := q.k.staking.UnbondingTime(ctx)
	if err != nil {
		return 0, err
	}
	end := epoch.EndTime
	if now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix(); end < now {
		end = now // the epoch end is due (its sweep may span blocks)
	}
	return end + int64(unbondingSeconds(ut)), nil
}

// heldCompletion is when a retired vintage's held value completes:
// UNBONDING, the record's completion; PENDING, pendingCompletion; else 0.
func (q queryServer) heldCompletion(ctx context.Context, r *types.UnbondRecord) (int64, error) {
	if r == nil {
		return 0, nil
	}
	switch r.Status {
	case types.UNBOND_STATUS_UNBONDING:
		return r.CompletionTime / int64(time.Second), nil
	case types.UNBOND_STATUS_PENDING:
		return q.pendingCompletion(ctx)
	default:
		return 0, nil
	}
}

// VintageInfo is the vintage clock.
func (q queryServer) VintageInfo(ctx context.Context, _ *types.QueryVintageInfoRequest) (*types.QueryVintageInfoResponse, error) {
	vc, err := q.k.vintageCtx(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryVintageInfoResponse{CurrentVintage: vc.Current, BaseVintage: vc.Base,
		NextRetirement: types.VintageStart(types.AddMonths(vc.Current, 1)).Unix()}, nil
}

// vintageBookKey is Query/VintageBooks' page key: a (validator, vintage)
// in the stores' own key encoding and order.
var vintageBookKey = collections.PairKeyCodec(collections.StringKey, collections.Uint32Key)

// VintageBooks is every validator's vintages, one page of the union of the
// stored (live or retiring) and retired vintages in (validator, vintage)
// order; no key is in both (invariant 11).
func (q queryServer) VintageBooks(ctx context.Context, req *types.QueryVintageBooksRequest) (*types.QueryVintageBooksResponse, error) {
	vc, err := q.k.vintageCtx(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	limit := uint64(MaxVintagesPage)
	var rng *collections.Range[collections.Pair[string, uint32]]
	if p := req.GetPagination(); p != nil {
		if p.Offset != 0 {
			return nil, status.Error(codes.InvalidArgument, "offset is not supported: page with next_key")
		}
		if p.Limit != 0 && p.Limit < limit {
			limit = p.Limit
		}
		if len(p.Key) > 0 {
			n, start, err := vintageBookKey.Decode(p.Key)
			if err != nil || n != len(p.Key) {
				return nil, status.Error(codes.InvalidArgument, "malformed page key")
			}
			rng = new(collections.Range[collections.Pair[string, uint32]]).StartInclusive(start)
		}
	}
	res := &types.QueryVintageBooksResponse{CurrentVintage: vc.Current, BaseVintage: vc.Base,
		NextRetirement: types.VintageStart(types.AddMonths(vc.Current, 1)).Unix(), Pagination: &query.PageResponse{}}
	type item struct {
		key collections.Pair[string, uint32]
		s   math.Int
		rv  *types.RetiredVintage
	}
	var stored, retired []item
	var ranger collections.Ranger[collections.Pair[string, uint32]]
	if rng != nil {
		ranger = rng
	}
	if err := q.k.VintageSupplies.Walk(ctx, ranger, func(key collections.Pair[string, uint32], s math.Int) (bool, error) {
		stored = append(stored, item{key: key, s: s})
		return uint64(len(stored)) > limit, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := q.k.RetiredVintages.Walk(ctx, ranger, func(key collections.Pair[string, uint32], rv types.RetiredVintage) (bool, error) {
		retired = append(retired, item{key: key, rv: &rv})
		return uint64(len(retired)) > limit, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	less := func(a, b collections.Pair[string, uint32]) bool {
		if a.K1() != b.K1() {
			return a.K1() < b.K1()
		}
		return a.K2() < b.K2()
	}
	var merged []item
	for i, j := 0, 0; i < len(stored) || j < len(retired); {
		if j == len(retired) || (i < len(stored) && less(stored[i].key, retired[j].key)) {
			merged = append(merged, stored[i])
			i++
		} else {
			merged = append(merged, retired[j])
			j++
		}
	}
	if uint64(len(merged)) > limit {
		next := merged[limit].key
		bz := make([]byte, vintageBookKey.Size(next))
		if _, err := vintageBookKey.Encode(bz, next); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		res.Pagination.NextKey = bz
		merged = merged[:limit]
	}
	for _, it := range merged {
		v, m := it.key.K1(), it.key.K2()
		e := types.VintageBookEntry{Validator: v, Vintage: m, Denom: types.VintageDenom(v, m, vc.Base),
			RetiresAt: types.VintageRetiresAt(m).Unix()}
		switch {
		case it.rv != nil:
			e.State, e.Retired = "retired", it.rv
			if it.rv.RemainingValue.IsPositive() {
				r, err := q.k.UnbondRecords.Get(ctx, collections.Join(v, it.rv.Epoch))
				if err != nil {
					return nil, status.Error(codes.Internal, err.Error())
				}
				e.Record = &r
			}
			if e.ExpectedCompletion, err = q.heldCompletion(ctx, e.Record); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		case types.VintageLive(m, vc.Current):
			e.State, e.Supply = "live", &it.s
		default:
			e.State, e.Supply = "retiring", &it.s
			if e.ExpectedCompletion, err = q.pendingCompletion(ctx); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		}
		res.Entries = append(res.Entries, e)
	}
	return res, nil
}
