package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// InitGenesis loads the books. It runs after bank, staking and shielded, and
// checks the books against them.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	if err := k.checkGenesisValidators(ctx, gs); err != nil {
		return err
	}
	if err := k.checkUnbondingEntries(ctx, gs.Params); err != nil {
		return err
	}
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
	if err := k.PositionCount.Set(ctx, uint64(len(gs.Positions))); err != nil {
		return err
	}
	if err := k.SnapshotSeq.Set(ctx, gs.SnapshotSeq); err != nil {
		return err
	}
	for _, c := range gs.SupplyCheckpoints {
		if err := k.SupplyCheckpoints.Set(ctx, collections.Join(c.Validator, c.Seq), c.Supply); err != nil {
			return err
		}
		if err := k.CheckpointsBySeq.Set(ctx, collections.Join(c.Seq, c.Validator)); err != nil {
			return err
		}
	}
	if gs.EpochSweep != nil {
		if err := k.EpochSweep.Set(ctx, *gs.EpochSweep); err != nil {
			return err
		}
	}
	for _, s := range gs.Snapshots {
		if err := k.Snapshots.Set(ctx, s.ProposalId, s); err != nil {
			return err
		}
		if err := k.SnapshotExpiry.Set(ctx, collections.Join(s.VotingEnd, s.ProposalId)); err != nil {
			return err
		}
		if s.Seq > 0 {
			if err := k.SnapshotsBySeq.Set(ctx, collections.Join(s.Seq, s.ProposalId)); err != nil {
				return err
			}
		}
	}
	for _, v := range gs.Votes {
		if err := k.putVote(ctx, v); err != nil {
			return err
		}
	}
	if err := k.initStakeTree(ctx, gs); err != nil {
		return err
	}
	if err := k.initGenesisEscrows(ctx); err != nil {
		return err
	}
	return k.AssertInvariants(ctx)
}

// checkUnbondingEntries refuses params under which an epoch's undelegation
// could hit x/staking's max_entries for the module's delegation to one
// validator (audit F8): one entry per epoch over the unbonding time, plus
// one for a sweep under way.
func (k Keeper) checkUnbondingEntries(ctx context.Context, p types.Params) error {
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return err
	}
	maxEntries, err := k.staking.MaxEntries(ctx)
	if err != nil {
		return err
	}
	if need := p.MaxEpochUnbondings(uint64(ut / time.Second)); need > uint64(maxEntries) {
		return types.ErrInvalidMsg.Wrapf("epoch_seconds %d with x/staking's unbonding_time %s needs max_entries >= %d, have %d",
			p.EpochSeconds, ut, need, maxEntries)
	}
	return nil
}

// checkGenesisValidators refuses a genesis whose books, records, positions,
// snapshots or votes name a validator by any string but its canonical
// encoding under this chain's validator codec (keeper.valAddr).
func (k Keeper) checkGenesisValidators(_ context.Context, gs types.GenesisState) error {
	check := func(what, v string) error {
		if _, err := k.valAddr(v); err != nil {
			return fmt.Errorf("genesis %s: %w", what, err)
		}
		return nil
	}
	for _, v := range gs.Validators {
		if err := check("book", v.Validator); err != nil {
			return err
		}
	}
	for _, r := range gs.UnbondRecords {
		if err := check("unbond record", r.Validator); err != nil {
			return err
		}
	}
	for _, p := range gs.Positions {
		if err := check("position", p.Validator); err != nil {
			return err
		}
	}
	for _, s := range gs.Snapshots {
		for _, vs := range s.Validators {
			if err := check("snapshot", vs.Validator); err != nil {
				return err
			}
		}
	}
	for _, v := range gs.Votes {
		if err := check("vote", v.Validator); err != nil {
			return err
		}
	}
	for _, c := range gs.SupplyCheckpoints {
		if err := check("supply checkpoint", c.Validator); err != nil {
			return err
		}
	}
	if gs.EpochSweep != nil && gs.EpochSweep.Cursor != "" {
		if err := check("epoch sweep cursor", gs.EpochSweep.Cursor); err != nil {
			return err
		}
	}
	return nil
}

// initStakeTree rebuilds the stake note tree from its leaves, its nullifier
// set and its roots, the last of which is the latest and must be the
// rebuilt tree's root.
func (k Keeper) initStakeTree(ctx context.Context, gs types.GenesisState) error {
	t, err := k.stakeTree(ctx)
	if err != nil {
		return err
	}
	for i, cm := range gs.StakeCommitments {
		leaf, err := privacy.FieldFromBytes(cm)
		if err != nil {
			return fmt.Errorf("stake commitment %d: %w", i, err)
		}
		if _, err := t.Append(leaf); err != nil {
			return err
		}
	}
	if err := k.StakeTreeSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	for _, nf := range gs.StakeNullifiers {
		if err := k.StakeNullifiers.Set(ctx, nf); err != nil {
			return err
		}
	}
	for i, r := range gs.StakeRoots {
		if err := k.putStakeRoot(ctx, r, i == len(gs.StakeRoots)-1); err != nil {
			return err
		}
	}
	if t.Size() == 0 {
		return nil
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, privacy.FieldBytes(root)) {
		return fmt.Errorf("the stake tree's root %X is not its latest recorded root %X", privacy.FieldBytes(root), latest)
	}
	return nil
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
	if gs.SnapshotSeq, err = k.SnapshotSeq.Peek(ctx); err != nil {
		return nil, err
	}
	if err := k.SupplyCheckpoints.Walk(ctx, nil, func(key collections.Pair[string, uint64], supply math.Int) (bool, error) {
		gs.SupplyCheckpoints = append(gs.SupplyCheckpoints, types.SupplyCheckpoint{Validator: key.K1(), Seq: key.K2(), Supply: supply})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if sweep, err := k.EpochSweep.Get(ctx); err == nil {
		if sweep.Active {
			gs.EpochSweep = &sweep
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
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
	t, err := k.stakeTree(ctx)
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < t.Size(); i++ {
		l, err := t.Leaf(i)
		if err != nil {
			return nil, err
		}
		gs.StakeCommitments = append(gs.StakeCommitments, privacy.FieldBytes(l))
	}
	if err := k.StakeNullifiers.Walk(ctx, nil, func(nf []byte) (bool, error) {
		gs.StakeNullifiers = append(gs.StakeNullifiers, nf)
		return false, nil
	}); err != nil {
		return nil, err
	}
	// Roots oldest first, the latest last (InitGenesis makes the last one
	// the latest).
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	var last *types.StakeRoot
	if err := k.StakeRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		r, err := k.StakeRoots.Get(ctx, key.K2())
		if err != nil {
			return true, err
		}
		if bytes.Equal(r.Root, latest) {
			last = &r
			return false, nil
		}
		gs.StakeRoots = append(gs.StakeRoots, r)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if last != nil {
		gs.StakeRoots = append(gs.StakeRoots, *last)
	}
	return gs, nil
}
