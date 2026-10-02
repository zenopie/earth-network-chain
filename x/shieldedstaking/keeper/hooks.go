package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// StakingHooks do two things.
//
// They close transparent delegation. Every delegation change in x/staking —
// MsgDelegate, MsgUndelegate, MsgBeginRedelegate, MsgCancelUnbondingDelegation,
// from a signed tx, authz, a group or gov proposal, an ICA host tx or a
// contract — calls BeforeDelegationCreated or BeforeDelegationSharesModified
// before it moves anything, and an error there aborts it. Only two delegators
// pass: this module, and a validator operator on its own validator (the public
// self-bond, including MsgCreateValidator's). The tally and the privacy model
// both assume the module is the only other delegator. The ante filter in
// x/shieldedstaking/ante says the same thing earlier, with a clearer error;
// this is the check nothing can route around.
//
// And they pass a slash through to the epoch's pending undelegations
// (BeforeValidatorSlashed): unbond notes minted this epoch are still bonded
// until the epoch ends, so their target shrinks by the fraction the slash
// takes from the validator's tokens.
type StakingHooks struct{ k Keeper }

var _ stakingtypes.StakingHooks = StakingHooks{}

// StakingHooks returns the hooks to register with x/staking.
func (k Keeper) StakingHooks() StakingHooks { return StakingHooks{k: k} }

// AllowedDelegator reports whether del may hold a delegation to val.
func (k Keeper) AllowedDelegator(del sdk.AccAddress, val sdk.ValAddress) bool {
	return del.Equals(k.modAddr) || sdk.AccAddress(val).Equals(del)
}

func (h StakingHooks) guard(del sdk.AccAddress, val sdk.ValAddress) error {
	if h.k.AllowedDelegator(del, val) {
		return nil
	}
	return types.ErrTransparentStaking
}

func (h StakingHooks) BeforeDelegationCreated(_ context.Context, del sdk.AccAddress, val sdk.ValAddress) error {
	return h.guard(del, val)
}

func (h StakingHooks) BeforeDelegationSharesModified(_ context.Context, del sdk.AccAddress, val sdk.ValAddress) error {
	return h.guard(del, val)
}

// BeforeValidatorSlashed haircuts v's pending undelegations by fraction. It
// never fails the slash.
func (h StakingHooks) BeforeValidatorSlashed(ctx context.Context, val sdk.ValAddress, fraction math.LegacyDec) error {
	k := h.k
	valoper, err := k.staking.ValidatorAddressCodec().BytesToString(val)
	if err != nil {
		return nil
	}
	err = k.guarded(ctx, func(cc context.Context) error {
		records, err := k.pendingRecords(cc, valoper)
		if err != nil || len(records) == 0 {
			return err
		}
		keep := math.LegacyOneDec().Sub(fraction)
		if keep.IsNegative() {
			keep = math.LegacyZeroDec()
		}
		total := math.ZeroInt()
		for _, r := range records {
			r.Target = keep.MulInt(r.Target).TruncateInt()
			total = total.Add(r.Target)
			if err := k.UnbondRecords.Set(cc, collections.Join(r.Validator, r.Epoch), r); err != nil {
				return err
			}
		}
		vs, err := k.ValidatorState(cc, valoper)
		if err != nil {
			return err
		}
		vs.PendingUndelegation = total
		if err := k.Validators.Set(cc, valoper, vs); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(cc).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSlashHaircut,
			sdk.NewAttribute(types.AttributeKeyValidator, valoper),
			sdk.NewAttribute(types.AttributeKeyFraction, fraction.String()),
			sdk.NewAttribute(types.AttributeKeyAmount, total.String()),
		))
		return nil
	})
	if err != nil {
		k.failure(ctx, "slash_haircut", valoper, err)
	}
	// The slash has not moved the tokens yet: re-weigh v's positions at the
	// end of the block (reweighSlashed), with x/allocation's resync of the
	// operator's self-bond.
	if err := k.SlashedValidators.Set(ctx, valoper); err != nil {
		k.failure(ctx, "slash_record", valoper, err)
	}
	return nil
}

// AfterValidatorCreated refuses a validator whose operator already pays its
// rewards to another account: its self-bond could not compound.
func (h StakingHooks) AfterValidatorCreated(ctx context.Context, val sdk.ValAddress) error {
	return h.k.checkOperatorWithdrawAddr(ctx, sdk.AccAddress(val))
}
func (StakingHooks) BeforeValidatorModified(context.Context, sdk.ValAddress) error { return nil }
func (StakingHooks) AfterValidatorRemoved(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterValidatorBonded(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterValidatorBeginUnbonding(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) BeforeDelegationRemoved(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterDelegationModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
