package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Zero-height export (app.prepForZeroHeightGenesis) withdraws every
// delegator's rewards and resets x/staking's unbonding entries' creation
// heights to 0. Without these two hooks the module's books would not survive
// it (audit F3): the rewards would land in the module account unbooked
// (invariant 1), and the unbonding records would name heights no entry has
// (invariant 3, and matureRecords would find nothing to pay).

// BookRewardsForZeroHeight withdraws the module's rewards at every
// validator it delegates to and books each into that validator's queue, as
// an epoch end would. Run it before the app withdraws all delegators'
// rewards (the module's then come to nothing).
func (k Keeper) BookRewardsForZeroHeight(ctx context.Context) error {
	var vals []sdk.ValAddress
	if err := k.staking.IterateDelegations(ctx, k.modAddr, func(_ int64, d stakingtypes.DelegationI) bool {
		bz, err := k.staking.ValidatorAddressCodec().StringToBytes(d.GetValidatorAddr())
		if err == nil {
			vals = append(vals, bz)
		}
		return false
	}); err != nil {
		return err
	}
	for _, val := range vals {
		valoper, err := k.staking.ValidatorAddressCodec().BytesToString(val)
		if err != nil {
			return err
		}
		vs, err := k.ValidatorState(ctx, valoper)
		if err != nil {
			return err
		}
		before := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
		if _, err := k.distr.WithdrawDelegationRewards(ctx, k.modAddr, val); err != nil {
			return err
		}
		got := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount.Sub(before)
		vs.PendingDelegation = vs.PendingDelegation.Add(got)
		if err := k.Validators.Set(ctx, valoper, vs); err != nil {
			return err
		}
	}
	return nil
}

// ResetHeightsForZeroHeight follows x/staking's reset of its unbonding and
// redelegation entries' creation heights to 0: every UNBONDING record's
// creation height becomes 0 too. Heights this module compares with the new
// chain's (a snapshot's height; a Groundworks vote's created_height) are
// shifted below 1 keeping their order: h becomes h - height - 1, with height
// the export height, so every vote or snapshot made on the new chain sorts
// after every old one.
//
// The open moves are dropped (audit 7, A7-2): their entries are at height 0
// now, which a slash on the new chain reaches only for a double sign at its
// first block (x/evidence slashes from the infraction height less one; that
// slash, should it ever come, falls on the destination's book), so there is
// nothing left for them to owe. Their
// debt rows stay: a label still clears against the debt tree alone, at its
// row's retained value, or at its whole exposure when the move was never
// slashed. The module's height-0 entries keep their shares until they
// mature, unowned (checkRedelegationRecord checks no shares at height 0).
func (k Keeper) ResetHeightsForZeroHeight(ctx context.Context, height int64) error {
	shift := func(h int64) int64 { return h - height - 1 }
	var moves []types.Move
	if err := k.Moves.Walk(ctx, nil, func(_ []byte, mv types.Move) (bool, error) {
		moves = append(moves, mv)
		return false, nil
	}); err != nil {
		return err
	}
	for _, mv := range moves {
		if err := k.removeMoveIndexes(ctx, mv); err != nil {
			return err
		}
		if err := k.Moves.Remove(ctx, mv.Key); err != nil {
			return err
		}
	}
	var recs []types.UnbondRecord
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		if r.Status == types.UNBOND_STATUS_UNBONDING {
			recs = append(recs, r)
		}
		return false, nil
	}); err != nil {
		return err
	}
	for _, r := range recs {
		r.CreationHeight = 0
		if err := k.UnbondRecords.Set(ctx, collections.Join(r.Validator, r.Epoch), r); err != nil {
			return err
		}
	}
	var gvs []types.GroundworksVote
	if err := k.GwVotes.Walk(ctx, nil, func(_ uint64, v types.GroundworksVote) (bool, error) {
		gvs = append(gvs, v)
		return false, nil
	}); err != nil {
		return err
	}
	for _, v := range gvs {
		v.CreatedHeight = shift(v.CreatedHeight)
		if err := k.setVote(ctx, v); err != nil {
			return err
		}
	}
	// A book's supply_height is compared with snapshot heights
	// (snapshotSupply): shifted alike.
	var books []types.ValidatorState
	if err := k.Validators.Walk(ctx, nil, func(_ string, vs types.ValidatorState) (bool, error) {
		books = append(books, vs)
		return false, nil
	}); err != nil {
		return err
	}
	for _, vs := range books {
		if vs.SupplyHeight == 0 {
			continue
		}
		vs.SupplyHeight = shift(vs.SupplyHeight)
		if err := k.Validators.Set(ctx, vs.Validator, vs); err != nil {
			return err
		}
	}
	var snaps []types.ProposalSnapshot
	if err := k.Snapshots.Walk(ctx, nil, func(_ uint64, s types.ProposalSnapshot) (bool, error) {
		snaps = append(snaps, s)
		return false, nil
	}); err != nil {
		return err
	}
	for _, s := range snaps {
		s.Height = shift(s.Height)
		if err := k.Snapshots.Set(ctx, s.ProposalId, s); err != nil {
			return err
		}
	}
	var roots []types.StakeRoot
	if err := k.StakeRoots.Walk(ctx, nil, func(_ []byte, r types.StakeRoot) (bool, error) {
		roots = append(roots, r)
		return false, nil
	}); err != nil {
		return err
	}
	for _, r := range roots {
		r.Height = shift(r.Height)
		if err := k.StakeRoots.Set(ctx, r.Root, r); err != nil {
			return err
		}
	}
	return nil
}
