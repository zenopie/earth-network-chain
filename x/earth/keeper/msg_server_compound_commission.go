package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/earth/types"
)

// CompoundCommission converts a validator's withheld commission into stake in
// their own validator.
//
// This is deliberately not a withdrawal. Commission never becomes liquid: the
// only way out is to compound it and then unbond, on the same terms as every
// other staker. Paying it out on demand would reopen the hole where capital runs
// a 100%-commission validator purely to turn staking yield back into something
// claimable instantly.
//
// The signer is validator_address, whose account address shares the same bytes,
// so authorisation is structural — only the operator can move their own ledger.
func (k msgServer) CompoundCommission(ctx context.Context, msg *types.MsgCompoundCommission) (*types.MsgCompoundCommissionResponse, error) {
	valAddr, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		// A fully removed validator cannot be delegated to, so the coins would have
		// nowhere to go. Failing loudly beats moving them somewhere unstakeable.
		return nil, errorsmod.Wrapf(types.ErrNoCommission, "validator %s not found", msg.ValidatorAddress)
	}

	amount, err := k.GetAccruedCommission(ctx, valAddr.Bytes())
	if err != nil {
		return nil, err
	}
	if !amount.IsPositive() {
		return nil, errorsmod.Wrapf(types.ErrNoCommission, "nothing accrued for %s", msg.ValidatorAddress)
	}

	denom, err := k.stakingKeeper.BondDenom(ctx)
	if err != nil {
		return nil, err
	}
	coins := sdk.NewCoins(sdk.NewCoin(denom, amount))

	// Clear the ledger before moving anything: if the delegation below fails the
	// whole message reverts, so the ledger cannot drain without stake appearing.
	if err := k.AccruedCommission.Remove(ctx, valAddr.Bytes()); err != nil {
		return nil, err
	}

	operator := sdk.AccAddress(valAddr)
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, operator, coins); err != nil {
		return nil, err
	}

	// Unbonded is the source status: the coins are liquid in the operator's
	// account for the instant between the transfer above and this call.
	if _, err := k.stakingKeeper.Delegate(
		ctx, operator, amount, stakingtypes.Unbonded, validator, true,
	); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"compound_commission",
			sdk.NewAttribute("validator", msg.ValidatorAddress),
			sdk.NewAttribute("amount", coins.String()),
		),
	)

	return &types.MsgCompoundCommissionResponse{Amount: amount}, nil
}
