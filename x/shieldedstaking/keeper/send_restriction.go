package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// SendRestriction keeps the module account's uerth balance exactly what its
// books say (see AssertInvariants): coins reach it only from the shielded
// pool (a private msg's value, through SpendToModule) and from x/distribution
// (rewards, which it pays on every delegation change). The account cannot be
// on the bank's blocked list — distribution pays it with
// SendCoinsFromModuleToAccount, which refuses blocked recipients — so without
// this a plain MsgSend could put unbooked ERTH there. Unbonding payouts arrive
// through the bank's UndelegateCoins, which no restriction sees.
//
// It also seals the validators' reward escrows (escrow.go): one takes coins
// only from x/distribution, and pays only its own operator (which only this
// module does: nobody holds the escrow's key).
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, _ sdk.Coins) (sdk.AccAddress, error) {
	if to.Equals(k.modAddr) && !from.Equals(k.poolAddr) && !from.Equals(k.distAddr) && !from.Equals(k.modAddr) {
		return to, types.ErrSendRestricted.Wrap("private staking's account takes coins only from the shielded pool and distribution")
	}
	if _, escrow, err := k.escrowOwner(ctx, to); err != nil {
		return to, err
	} else if escrow && !from.Equals(k.distAddr) {
		return to, types.ErrSendRestricted.Wrap("a validator's reward escrow takes coins only from distribution")
	}
	if val, escrow, err := k.escrowOwner(ctx, from); err != nil {
		return to, err
	} else if escrow && !to.Equals(sdk.AccAddress(val)) {
		return to, types.ErrSendRestricted.Wrap("a validator's reward escrow pays only its operator")
	}
	return to, nil
}
