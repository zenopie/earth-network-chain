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
	// through positions instead).
	if src, err := k.weightSource(types.STREAM_ID_GROUNDWORKS); err == nil {
		if bt, ok := src.(types.BondedTracker); ok && !bt.TracksBonded(addrBz) {
			return nil
		}
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

	if err := k.AdvanceIndex(ctx, types.STREAM_ID_GROUNDWORKS); err != nil {
		return err
	}

	weight, err := k.stakingKeeper.GetDelegatorBonded(ctx, delAddr)
	if err != nil {
		return err
	}

	// BeforeDelegationRemoved fires while the delegation still counts toward
	// bonded, so subtract the tokens that are about to leave.
	if removeVal != nil {
		if del, err := k.stakingKeeper.GetDelegation(ctx, delAddr, *removeVal); err == nil {
			if val, err := k.stakingKeeper.GetValidator(ctx, *removeVal); err == nil && val.IsBonded() {
				weight = weight.Sub(val.TokensFromShares(del.Shares).TruncateInt())
			}
		}
	}
	if weight.IsNegative() {
		weight = math.ZeroInt()
	}

	return k.resyncVoter(ctx, types.STREAM_ID_GROUNDWORKS, addrBz, voter.Percentages, weight)
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
	if err := h.k.SlashedValidators.Set(ctx, valAddr.Bytes()); err != nil {
		sdk.UnwrapSDKContext(ctx).Logger().Error("allocation: could not record a slashed validator", "validator", valAddr.String(), "err", err)
	}
	return nil
}

// ResyncSlashed re-weighs, at their post-slash bonded stake, the operators of
// the validators slashed this block (a self-bond is an operator's Groundworks
// weight), then forgets them. Each in its own cache; a failure is logged and
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
func (h Hooks) AfterValidatorBonded(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterValidatorBeginUnbonding(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationCreated(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationSharesModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
