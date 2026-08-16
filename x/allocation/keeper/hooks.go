package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/allocation/types"
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
	voter, err := k.Voters.Get(ctx, voterKey(types.STREAM_ID_CAPITAL, addrBz))
	if err != nil {
		return nil // not a voter (or not found) — nothing to do
	}

	if err := k.AdvanceIndex(ctx, types.STREAM_ID_CAPITAL); err != nil {
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

	// The stream stores normalized units. Doing the conversion here rather than
	// leaving live tokens in state is what keeps a voter who touches their
	// delegation on the same footing as one who has not voted since before the
	// stake compounded.
	weight, err = k.earthKeeper.NormalizeStakeWeight(ctx, weight)
	if err != nil {
		return err
	}

	return k.resyncVoter(ctx, types.STREAM_ID_CAPITAL, addrBz, voter.Percentages, weight)
}

func (h Hooks) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, _ sdk.ValAddress) error {
	return h.k.resyncFromBonded(ctx, delAddr, nil)
}

func (h Hooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return h.k.resyncFromBonded(ctx, delAddr, &valAddr)
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
func (h Hooks) BeforeValidatorSlashed(context.Context, sdk.ValAddress, math.LegacyDec) error {
	return nil
}
func (h Hooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
