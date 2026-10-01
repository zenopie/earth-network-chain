package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// InitGenesis loads the books. It runs after bank, staking and shielded, and
// checks the books against them.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	if err := k.Params.Set(ctx, gs.Params); err != nil {
		return err
	}
	epoch := gs.Epoch
	if epoch == nil {
		now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
		epoch = &types.Epoch{Number: 1, StartTime: now, EndTime: now + int64(gs.Params.EpochSeconds)}
	}
	if err := k.Epoch.Set(ctx, *epoch); err != nil {
		return err
	}
	for _, v := range gs.Validators {
		if err := k.Validators.Set(ctx, v.Validator, v); err != nil {
			return err
		}
	}
	for _, r := range gs.UnbondRecords {
		key := collections.Join(r.Validator, r.Epoch)
		if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
			return err
		}
		switch r.Status {
		case types.UNBOND_STATUS_PENDING:
			if err := k.PendingRecords.Set(ctx, key); err != nil {
				return err
			}
		case types.UNBOND_STATUS_UNBONDING:
			if err := k.MaturityQueue.Set(ctx, collections.Join3(r.CompletionTime, r.Validator, r.Epoch)); err != nil {
				return err
			}
		}
	}
	for _, p := range gs.Positions {
		if err := k.setPosition(ctx, p); err != nil {
			return err
		}
		if err := k.PositionsByVal.Set(ctx, collections.Join(p.Validator, p.Id)); err != nil {
			return err
		}
	}
	if err := k.PositionSeq.Set(ctx, gs.NextPositionId); err != nil {
		return err
	}
	for _, s := range gs.Snapshots {
		if err := k.Snapshots.Set(ctx, s.ProposalId, s); err != nil {
			return err
		}
		if err := k.SnapshotExpiry.Set(ctx, collections.Join(s.VotingEnd, s.ProposalId)); err != nil {
			return err
		}
	}
	for _, v := range gs.Votes {
		if err := k.putVote(ctx, v); err != nil {
			return err
		}
	}
	return k.AssertInvariants(ctx)
}

// ExportGenesis exports the books.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	gs := &types.GenesisState{Params: params}
	if e, err := k.Epoch.Get(ctx); err == nil {
		gs.Epoch = &e
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.Validators.Walk(ctx, nil, func(_ string, v types.ValidatorState) (bool, error) {
		gs.Validators = append(gs.Validators, v)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		gs.UnbondRecords = append(gs.UnbondRecords, r)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.Positions.Walk(ctx, nil, func(_ uint64, p types.Position) (bool, error) {
		gs.Positions = append(gs.Positions, p)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if gs.NextPositionId, err = k.PositionSeq.Peek(ctx); err != nil {
		return nil, err
	}
	if err := k.Snapshots.Walk(ctx, nil, func(_ uint64, s types.ProposalSnapshot) (bool, error) {
		gs.Snapshots = append(gs.Snapshots, s)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.Votes.Walk(ctx, nil, func(_ collections.Pair[uint64, []byte], v types.StakeVote) (bool, error) {
		gs.Votes = append(gs.Votes, v)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return gs, nil
}
