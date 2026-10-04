package keeper

import (
	"context"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	vestingexported "github.com/cosmos/cosmos-sdk/x/auth/vesting/exported"
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
// (BeforeValidatorSlashed): undelegations booked this epoch are still bonded
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

// AfterValidatorCreated points the new validator's operator at its reward
// escrow (escrow.go), before MsgCreateValidator's self-delegation, and
// refuses an operator that already pays its rewards to another account.
//
// It also refuses a vesting account as the operator (refuseVestingOperator).
func (h StakingHooks) AfterValidatorCreated(ctx context.Context, val sdk.ValAddress) error {
	if err := h.k.refuseVestingOperator(ctx, val); err != nil {
		return err
	}
	// A validator re-created at an operator whose previous validator's
	// escrow release is still queued: the escrow is this validator's again,
	// and the epoch end's retry must not release it out from under it.
	if err := h.k.PendingReleases.Remove(ctx, val); err != nil {
		return err
	}
	return h.k.setOperatorEscrow(ctx, val, true)
}

// refuseVestingOperator refuses a validator whose operator is a vesting
// account. The epoch end compounds the operator's rewards by delegating coins
// it has just been paid, and x/bank tracks any delegation from a vesting
// account against its vesting coins first (TrackDelegation): every compounded
// reward would free as much of the vesting balance, until none is left
// locked. Refused at creation (the hook) and in genesis (initGenesisEscrows).
// x/auth/vesting creates vesting accounts only at new addresses, so an
// operator cannot become one later.
func (k Keeper) refuseVestingOperator(ctx context.Context, val sdk.ValAddress) error {
	acc := k.auth.GetAccount(ctx, sdk.AccAddress(val))
	if acc == nil {
		return nil
	}
	if _, vesting := acc.(vestingexported.VestingAccount); vesting {
		return errorsmod.Wrapf(types.ErrVestingOperator, "operator %s", sdk.AccAddress(val))
	}
	return nil
}

// BeforeValidatorModified: x/staking calls it as a slash begins (and on
// MsgEditValidator). If the module redelegated from val, the slash may take
// from its redelegation entries, unbonding the module's delegation at their
// destinations: prepareRedelegationSlash books the rewards that would pay
// and re-weighs those destinations. Never fails.
func (h StakingHooks) BeforeValidatorModified(ctx context.Context, val sdk.ValAddress) error {
	h.k.prepareRedelegationSlash(ctx, val)
	return nil
}

// AfterValidatorRemoved releases the removed validator's reward escrow to its
// operator (escrow.go). x/staking calls it from its EndBlocker, so it never
// fails: a failure is logged and the escrow keeps its balance.
//
// A failed release is queued (PendingReleases) and retried at each epoch
// end; until it succeeds the escrow stays recorded, and invariant 5 accepts
// it for a removed validator.
func (h StakingHooks) AfterValidatorRemoved(ctx context.Context, _ sdk.ConsAddress, val sdk.ValAddress) error {
	if err := h.k.guarded(ctx, func(cc context.Context) error { return h.k.releaseEscrow(cc, val) }); err != nil {
		h.k.failure(ctx, "escrow_release", val.String(), err)
		if err := h.k.PendingReleases.Set(ctx, val); err != nil {
			h.k.failure(ctx, "escrow_release", val.String(), err)
		}
	}
	return nil
}
func (StakingHooks) AfterValidatorBonded(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterValidatorBeginUnbonding(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}

// BeforeDelegationRemoved schedules the retirement of an operator that
// removes its whole self-bond (escrow.go). Never fails the undelegation.
func (h StakingHooks) BeforeDelegationRemoved(ctx context.Context, del sdk.AccAddress, val sdk.ValAddress) error {
	if !sdk.AccAddress(val).Equals(del) {
		return nil
	}
	if err := h.k.guarded(ctx, func(cc context.Context) error { return h.k.scheduleRetirement(cc, val) }); err != nil {
		h.k.failure(ctx, "escrow_retire", val.String(), err)
	}
	return nil
}
func (StakingHooks) AfterDelegationModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (StakingHooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
