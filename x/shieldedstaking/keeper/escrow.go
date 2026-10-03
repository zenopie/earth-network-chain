package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Reward escrows (see withdraw_addr.go for why). Each validator has one,
// types.RewardEscrowAddress(val), recorded in RewardEscrows from its
// creation to its removal. Coins enter only from x/distribution (the
// operator's self-bond rewards, the validator's commission) and leave only
// to the operator, moved by this module (SendRestriction):
//
//   - epoch end, active (bonded, unjailed) validator: the escrow's uerth goes
//     to the operator account and is self-delegated in the same cache
//     context (compoundSelfBond), so it is never liquid;
//   - a jailed validator's escrow keeps accruing (what self-bond changes pay
//     it) until the validator is active again;
//   - the validator is removed (x/staking removes it once it is unbonded and
//     holds no delegation: its operator undelegated the whole self-bond and
//     the unbonding period passed): everything left in the escrow — every
//     denom — is released to the operator (releaseEscrow). That is the
//     operator's exit, after unbonding, as for the self-bond itself.
//
// Non-uerth rewards (fee denoms) cannot be staked: they stay in the escrow
// until the validator is removed.

// escrowOwner reports whether addr is a validator's reward escrow, and
// whose.
func (k Keeper) escrowOwner(ctx context.Context, addr sdk.AccAddress) (sdk.ValAddress, bool, error) {
	val, err := k.RewardEscrows.Get(ctx, addr)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

// releaseEscrow pays everything in val's reward escrow to its operator,
// forgets the escrow and resets the operator's withdraw address to itself:
// the validator is gone.
func (k Keeper) releaseEscrow(ctx context.Context, val sdk.ValAddress) error {
	op, escrow := sdk.AccAddress(val), types.RewardEscrowAddress(val)
	if bal := k.bank.GetAllBalances(ctx, escrow); !bal.IsZero() {
		if err := k.bank.SendCoins(ctx, escrow, op, bal); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEscrowReleased,
			sdk.NewAttribute(types.AttributeKeyValidator, val.String()),
			sdk.NewAttribute(types.AttributeKeyAmount, bal.String()),
		))
	}
	if err := k.RewardEscrows.Remove(ctx, escrow); err != nil {
		return err
	}
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op)
	if err != nil {
		return err
	}
	if wa.Equals(escrow) {
		return k.distr.DeleteDelegatorWithdrawAddr(ctx, op, wa)
	}
	return nil
}
