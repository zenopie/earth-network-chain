package keeper

import (
	"context"
	"errors"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// A validator operator's self-bond always compounds (epoch.go), so its
// rewards must land in the operator account: an operator has no withdraw
// address of its own choosing. Four places hold that line:
//
//   - genesis: networks/genesis sets x/distribution's withdraw_addr_enabled
//     to false, so MsgSetWithdrawAddress fails for every account by every
//     route (tx, authz, group, gov, ICA, contract). Only operators and this
//     module hold delegations, so nobody else loses anything by it.
//   - the ante filter (x/shieldedstaking/ante) refuses an operator's
//     MsgSetWithdrawAddress, top level or inside authz MsgExec, with this
//     module's error — the line that still holds if governance re-enables
//     withdraw addresses.
//   - AfterValidatorCreated refuses a new validator whose operator already
//     set a withdraw address elsewhere; InitGenesis refuses a genesis that
//     has one.
//   - compoundSelfBond resets a foreign withdraw address it finds rather
//     than skipping the operator, so no route the refusals miss can opt a
//     self-bond out of compounding.
//
// The same holds for claiming: an operator's MsgWithdrawDelegatorReward and
// every MsgWithdrawValidatorCommission are refused, by the ante (top level,
// authz MsgExec) and by app's message router (app/operator_router.go), which
// authz dispatch, gov and group proposal execution, the ICA host and
// contracts all go through. Only a self-bond change reaches the rewards
// another way: x/distribution's delegation hook pays the self-bond's accrued
// rewards to the operator when its shares change (MsgDelegate/
// MsgUndelegate by the operator). They cannot be re-delegated in the same
// tx: x/staking writes the validator it read before the hooks after they
// return (Unbond -> RemoveValidatorTokensAndShares; Delegate ->
// AddValidatorTokensAndShares), so a nested Delegate from a hook would be
// clobbered. That leak is minor: it needs the operator to move its own
// self-bond, is at most the rewards accrued since the last epoch end, and
// commission is not paid by it.

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
// (genesis; x/distribution would refuse it too, with a less helpful error),
// and otherwise when del is a validator operator and withdraw is another
// account.
func (k Keeper) CheckWithdrawAddr(ctx context.Context, del, withdraw sdk.AccAddress) error {
	enabled, err := k.distr.GetWithdrawAddrEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return types.ErrOperatorWithdraw
	}
	if del.Equals(withdraw) {
		return nil
	}
	op, err := k.IsOperator(ctx, del)
	if err != nil {
		return err
	}
	if op {
		return errorsmod.Wrapf(types.ErrOperatorWithdraw, "operator %s cannot pay its rewards to %s", del, withdraw)
	}
	return nil
}

// checkOperatorWithdrawAddr refuses an operator whose stored withdraw
// address is another account.
func (k Keeper) checkOperatorWithdrawAddr(ctx context.Context, op sdk.AccAddress) error {
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op)
	if err != nil {
		return err
	}
	if !wa.Equals(op) {
		return errorsmod.Wrapf(types.ErrOperatorWithdraw, "operator %s has withdraw address %s", op, wa)
	}
	return nil
}

// checkGenesisWithdrawAddrs refuses a genesis in which any validator's
// operator has a withdraw address elsewhere (x/distribution's
// delegator_withdraw_infos). Runs after staking, distribution and genutil.
func (k Keeper) checkGenesisWithdrawAddrs(ctx context.Context) error {
	vals, err := k.staking.GetAllValidators(ctx)
	if err != nil {
		return err
	}
	for _, v := range vals {
		bz, err := k.staking.ValidatorAddressCodec().StringToBytes(v.GetOperator())
		if err != nil {
			return err
		}
		if err := k.checkOperatorWithdrawAddr(ctx, sdk.AccAddress(bz)); err != nil {
			return errorsmod.Wrap(err, "genesis")
		}
	}
	return nil
}

// resetOperatorWithdrawAddr points op's withdraw address back at op if it
// points elsewhere, and says so.
func (k Keeper) resetOperatorWithdrawAddr(ctx context.Context, op sdk.AccAddress) error {
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op)
	if err != nil {
		return err
	}
	if wa.Equals(op) {
		return nil
	}
	if err := k.distr.DeleteDelegatorWithdrawAddr(ctx, op, wa); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeWithdrawAddrReset,
		sdk.NewAttribute(types.AttributeKeyValidator, sdk.ValAddress(op).String()),
		sdk.NewAttribute(types.AttributeKeyWithdrawAddr, wa.String()),
	))
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
