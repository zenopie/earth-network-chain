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
func (k Keeper) SendRestriction(_ context.Context, from, to sdk.AccAddress, _ sdk.Coins) (sdk.AccAddress, error) {
	if to.Equals(k.modAddr) && !from.Equals(k.poolAddr) && !from.Equals(k.distAddr) && !from.Equals(k.modAddr) {
		return to, types.ErrSendRestricted.Wrap("private staking's account takes coins only from the shielded pool and distribution")
	}
	return to, nil
}
