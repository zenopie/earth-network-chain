package keeper

import (
	"context"
	"errors"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// A validator's income — its operator's self-bond rewards and its
// commission — compounds into the self-bond at every epoch end (epoch.go)
// and is never liquid. x/distribution pays both to the operator's withdraw
// address, from the explicit withdrawals and also as a side effect of every
// self-bond change (its BeforeDelegationSharesModified hook pays the
// rewards accrued since the last withdrawal). So the chain points every
// operator's withdraw address at the validator's REWARD ESCROW
// (types.RewardEscrowAddress, escrow.go), an account of this module's that
// nobody holds a key for: whatever distribution pays, by any path, waits
// there until the epoch end moves it into the self-bond. An operator moving
// its own self-bond (even 1uerth daily) only moves rewards into its escrow.
//
// The escrow is set when the validator is created (AfterValidatorCreated)
// and at InitGenesis, through x/distribution's store setter (bypassing
// withdraw_addr_enabled). The refusals stay, as defense in depth:
//
//   - genesis: networks/genesis sets x/distribution's withdraw_addr_enabled
//     to false, so MsgSetWithdrawAddress fails for every account by every
//     route (tx, authz, group, gov, ICA, contract). Only operators and this
//     module hold delegations, so nobody else loses anything by it.
//   - the ante filter (x/shieldedstaking/ante) and app's message router
//     refuse an operator's MsgSetWithdrawAddress to anything but its escrow,
//     and anyone's to another validator's escrow — the line that still
//     holds if governance re-enables withdraw addresses.
//   - AfterValidatorCreated refuses a new validator whose operator already
//     set a withdraw address elsewhere; InitGenesis and `genesis validate`
//     refuse a genesis that has one.
//   - compoundSelfBond resets a withdraw address it finds pointing anywhere
//     but the escrow rather than skipping the operator.
//
// The same holds for claiming: an operator's MsgWithdrawDelegatorReward and
// every MsgWithdrawValidatorCommission are refused, by the ante (top level,
// authz MsgExec) and by app's message router (app/operator_router.go), which
// authz dispatch, gov and group proposal execution, the ICA host and
// contracts all go through. They would only pay the escrow anyway.

// IsOperator reports whether acc is a validator's operator account.
func (k Keeper) IsOperator(ctx context.Context, acc sdk.AccAddress) (bool, error) {
	_, err := k.staking.GetValidator(ctx, sdk.ValAddress(acc))
	if errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return false, nil
	}
	return err == nil, err
}

// CheckWithdrawAddr refuses del setting its withdraw address to withdraw:
// for every account while x/distribution's withdraw_addr_enabled is false
// (genesis; x/distribution would refuse it too, with a less helpful error);
// otherwise when del is a validator operator and withdraw is not its reward
// escrow (the operator itself included), and when withdraw is another
// validator's reward escrow (only distribution may pay one, and only for
// its own operator).
func (k Keeper) CheckWithdrawAddr(ctx context.Context, del, withdraw sdk.AccAddress) error {
	enabled, err := k.distr.GetWithdrawAddrEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return types.ErrOperatorWithdraw
	}
	own := types.RewardEscrowAddress(sdk.ValAddress(del))
	if withdraw.Equals(own) {
		return nil
	}
	if _, escrow, err := k.escrowOwner(ctx, withdraw); err != nil {
		return err
	} else if escrow {
		return errorsmod.Wrapf(types.ErrOperatorWithdraw, "%s is a validator's reward escrow", withdraw)
	}
	op, err := k.IsOperator(ctx, del)
	if err != nil {
		return err
	}
	if op {
		return errorsmod.Wrapf(types.ErrOperatorWithdraw, "operator %s cannot pay its rewards to %s (only to its reward escrow %s)", del, withdraw, own)
	}
	return nil
}

// setOperatorEscrow records val's reward escrow and points its operator's
// withdraw address at it. A withdraw address elsewhere (neither the
// operator, the default, nor the escrow) is refused when refuse is set
// (validator creation, genesis), and otherwise reset to the escrow, with an
// event (the epoch end: an address set by a route the refusals missed).
func (k Keeper) setOperatorEscrow(ctx context.Context, val sdk.ValAddress, refuse bool) error {
	op, escrow := sdk.AccAddress(val), types.RewardEscrowAddress(val)
	if err := k.RewardEscrows.Set(ctx, escrow, val); err != nil {
		return err
	}
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op)
	if err != nil {
		return err
	}
	if wa.Equals(escrow) {
		return nil
	}
	if !wa.Equals(op) {
		if refuse {
			return errorsmod.Wrapf(types.ErrOperatorWithdraw, "operator %s has withdraw address %s", op, wa)
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeWithdrawAddrReset,
			sdk.NewAttribute(types.AttributeKeyValidator, val.String()),
			sdk.NewAttribute(types.AttributeKeyWithdrawAddr, wa.String()),
		))
	}
	return k.distr.SetDelegatorWithdrawAddr(ctx, op, escrow)
}

// initGenesisEscrows points every validator's operator at its reward
// escrow, refusing a genesis in which one has a withdraw address elsewhere
// (x/distribution's delegator_withdraw_infos). Runs after staking,
// distribution and genutil; an exported genesis already carries the
// escrows (x/distribution exports them).
func (k Keeper) initGenesisEscrows(ctx context.Context) error {
	vals, err := k.staking.GetAllValidators(ctx)
	if err != nil {
		return err
	}
	for _, v := range vals {
		bz, err := k.staking.ValidatorAddressCodec().StringToBytes(v.GetOperator())
		if err != nil {
			return err
		}
		if err := k.refuseVestingOperator(ctx, bz); err != nil {
			return errorsmod.Wrap(err, "genesis")
		}
		if err := k.setOperatorEscrow(ctx, bz, true); err != nil {
			return errorsmod.Wrap(err, "genesis")
		}
		// An operator already without a self-bond retires a full
		// unbonding time from now (escrow.go).
		if _, err := k.staking.GetDelegation(ctx, sdk.AccAddress(bz), bz); errors.Is(err, stakingtypes.ErrNoDelegation) {
			if err := k.scheduleRetirement(ctx, bz); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// CheckRewardWithdraw refuses MsgWithdrawDelegatorReward from a validator
// operator: its self-bond rewards compound at the epoch end
// (compoundSelfBond), and a mid-epoch claim would leave nothing to compound.
// Commission compounds too; MsgWithdrawValidatorCommission is refused for
// every validator (x/shieldedstaking/ante.CheckOperatorRewardsMsg).
// Operators can only delegate to their own validator, so the validator is
// not consulted.
func (k Keeper) CheckRewardWithdraw(ctx context.Context, del sdk.AccAddress) error {
	op, err := k.IsOperator(ctx, del)
	if err != nil {
		return err
	}
	if op {
		return errorsmod.Wrapf(types.ErrOperatorRewardClaim, "operator %s", del)
	}
	return nil
}
