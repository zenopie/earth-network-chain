package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/allocation/types"

	"github.com/earth-network/earth/internal/safeexec"
)

// Hooks implements staking hooks that keep each capital-stream voter's weight in
// sync with their live bonded stake. They serve that stream only: a human's
// weight does not move when they delegate.
type Hooks struct {
	k Keeper
}

var _ stakingtypes.StakingHooks = Hooks{}

// Hooks returns the staking hooks for the allocation module.
func (k Keeper) Hooks() Hooks { return Hooks{k: k} }

// resyncFromBonded re-applies an existing capital voter's split at their current
// bonded weight, optionally subtracting a delegation that is about to be
// removed. It is a no-op for addresses that have not voted.
func (k Keeper) resyncFromBonded(ctx context.Context, delAddr sdk.AccAddress, removeVal *sdk.ValAddress) error {
	addrBz := delAddr.Bytes()
	// The stream's weight source decides whose bonded stake is weight (on
	// this chain: a validator operator's self-bond, never the private staking
	// module's own delegations, which change every epoch and are counted
	// through its Groundworks votes instead).
	if src, err := k.weightSource(types.STREAM_ID_GROUNDWORKS); err == nil {
		if bt, ok := src.(types.BondedTracker); ok && !bt.TracksBonded(addrBz) {
			return nil
		}
	}
	// Settled before the voter is read, so nothing between the read and the
	// write below moves it (a settle never retires a lease anyway: only the
	// BeginBlock sweep does; audit round 2, CD-1).
	if err := k.AdvanceIndex(ctx, types.STREAM_ID_GROUNDWORKS); err != nil {
		return err
	}
	voter, err := k.Voters.Get(ctx, voterKey(types.STREAM_ID_GROUNDWORKS, addrBz))
	if err != nil {
		return nil // not a voter (or not found) — nothing to do
	}

	// A split cast before the stream was last reset is not a vote any more.
	// resyncVoter already declines to subtract it, because the reset zeroed
	// the aggregates; but it then added it straight back at the new weight,
	// so the next delegation change undid governance's reset for that voter.
	// Drop the stale record instead, and let them vote again.
	epoch, err := k.getEpoch(ctx, types.STREAM_ID_GROUNDWORKS)
	if err != nil {
		return err
	}
	if voter.Epoch != epoch {
		return k.Voters.Remove(ctx, voterKey(types.STREAM_ID_GROUNDWORKS, addrBz))
	}

	weight, bondLeft, err := k.bondedWeight(ctx, delAddr, removeVal)
	if err != nil {
		return err
	}
	if weight.IsNegative() {
		weight = math.ZeroInt()
	}
	// A self-bond at a validator outside the active set weighs nothing, but
	// the vote stays (at weight zero) while the bond does: the weight comes
	// back when the validator is Bonded again, with no new vote.
	return k.resyncVoterKeep(ctx, types.STREAM_ID_GROUNDWORKS, addrBz, voter.Percentages, weight, bondLeft)
}

// bondedWeight is an account's Groundworks weight: its stake at Bonded
// validators (audit 7, D7-L2: a validator outside the active set, or
// tombstoned, secures nothing, and an Unbonded validator's bond can leave in
// one block), computed as GetDelegatorBonded computes its sum (each
// delegation's TokensFromSharesTruncated, summed, then rounded), leaving out
// the delegation to exclude, if any. bondLeft reports whether any
// delegation (at any status) remains.
//
// BeforeDelegationRemoved fires while the delegation is still stored, so the
// removed delegation is left out by name, whatever its validator's status.
//
// Status changes move weight now: a validator's operator is resynced when
// its validator becomes Bonded or starts unbonding (AfterValidatorBonded,
// AfterValidatorBeginUnbonding: recorded, resynced at EndBlock with the
// slashed ones). A slash changes tokens and shares, and ResyncSlashed
// re-weighs the operator at EndBlock. A redelegation is an Unbond
// (AfterDelegationModified, or BeforeDelegationRemoved) then a Delegate
// (AfterDelegationModified), each resynced in turn.
func (k Keeper) bondedWeight(ctx context.Context, delAddr sdk.AccAddress, exclude *sdk.ValAddress) (math.Int, bool, error) {
	bonded := math.LegacyZeroDec()
	left := false
	var inner error
	err := k.stakingKeeper.IterateDelegatorDelegations(ctx, delAddr, func(del stakingtypes.Delegation) bool {
		valAddr, err := sdk.ValAddressFromBech32(del.ValidatorAddress)
		if err != nil {
			inner = err
			return true
		}
		if exclude != nil && valAddr.Equals(*exclude) {
			return false
		}
		left = true
		if val, err := k.stakingKeeper.GetValidator(ctx, valAddr); err == nil && val.IsBonded() {
			bonded = bonded.Add(val.TokensFromSharesTruncated(del.Shares))
		}
		return false
	})
	if err == nil {
		err = inner
	}
	if err != nil {
		return math.Int{}, false, err
	}
	return bonded.RoundInt(), left, nil
}

// BondedWeight is an account's Groundworks weight from its own stake: its
// delegations at Bonded validators (bondedWeight). The stream's weight
// source uses it for a new vote.
func (k Keeper) BondedWeight(ctx context.Context, delAddr sdk.AccAddress) (math.Int, error) {
	w, _, err := k.bondedWeight(ctx, delAddr, nil)
	return w, err
}

func (h Hooks) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, _ sdk.ValAddress) error {
	return h.k.resyncFromBonded(ctx, delAddr, nil)
}

func (h Hooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return h.k.resyncFromBonded(ctx, delAddr, &valAddr)
}

// BeforeValidatorSlashed fires before the slash moves the validator's tokens,
// so the operator's self-bond weight cannot be read yet: the validator is
// recorded and EndBlock (ResyncSlashed) resyncs its operator once the slash
// has landed. Never errors: a slash must not fail over a weight record.
func (h Hooks) BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, _ math.LegacyDec) error {
	h.k.recordResync(ctx, valAddr)
	return nil
}

// recordResync marks valAddr's operator for re-weighing at EndBlock
// (ResyncSlashed). Never errors.
func (k Keeper) recordResync(ctx context.Context, valAddr sdk.ValAddress) {
	if err := k.SlashedValidators.Set(ctx, valAddr.Bytes()); err != nil {
		sdk.UnwrapSDKContext(ctx).Logger().Error("allocation: could not record a validator to resync", "validator", valAddr.String(), "err", err)
	}
}

// ResyncSlashed re-weighs, at their bonded stake now, the operators of the
// validators slashed, bonded or unbonding this block (a self-bond at a
// Bonded validator is an operator's Groundworks weight), then forgets them. Each in its own cache; a failure is logged and
// skipped, never returned: this runs in EndBlock.
func (k Keeper) ResyncSlashed(ctx context.Context) {
	var vals [][]byte
	_ = k.SlashedValidators.Walk(ctx, nil, func(v []byte) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, v := range vals {
		if err := safeexec.Cached(sdkCtx, func(cache sdk.Context) error {
			return k.resyncFromBonded(cache, sdk.AccAddress(v), nil)
		}); err != nil {
			sdkCtx.Logger().Error("allocation: post-slash resync failed", "operator", sdk.AccAddress(v).String(), "err", err)
		}
		_ = k.SlashedValidators.Remove(ctx, v)
	}
}

// --- remaining hooks are no-ops ---

func (h Hooks) AfterValidatorCreated(context.Context, sdk.ValAddress) error   { return nil }
func (h Hooks) BeforeValidatorModified(context.Context, sdk.ValAddress) error { return nil }
func (h Hooks) AfterValidatorRemoved(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}

// AfterValidatorBonded and AfterValidatorBeginUnbonding: the operator's
// self-bond starts or stops weighing (bondedWeight counts Bonded validators
// only). x/staking calls them from its EndBlocker; the operator is recorded
// and resynced at this module's EndBlock (ResyncSlashed, which runs after
// x/staking's). Never errors.
func (h Hooks) AfterValidatorBonded(ctx context.Context, _ sdk.ConsAddress, valAddr sdk.ValAddress) error {
	h.k.recordResync(ctx, valAddr)
	return nil
}

func (h Hooks) AfterValidatorBeginUnbonding(ctx context.Context, _ sdk.ConsAddress, valAddr sdk.ValAddress) error {
	h.k.recordResync(ctx, valAddr)
	return nil
}
func (h Hooks) BeforeDelegationCreated(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationSharesModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
