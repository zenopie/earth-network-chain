package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// SendRestriction is appended to the bank keeper's send restrictions (see
// app.New). It applies to every SendCoins, which is every transfer path the
// SDK has: MsgSend and MultiSend, module payouts, authz, group, vesting, ICA,
// IBC escrow, contracts. Minting and burning do not pass through it.
//
// Two rules:
//
//   - The pool account takes coins only from this keeper (MsgShield,
//     MintNote), which counts them into the turnstile as it moves them. The
//     account is deliberately not on the bank's blocked list — distribution
//     must be able to pay it once private staking lands — so this is what
//     keeps its balance exactly In - Out. A stray deposit would otherwise halt
//     the chain at the next invariant check.
//   - A shielded-only denom (uanml) may only go to the listed module
//     accounts: the pool itself and the modules that hold it in the open
//     (personhood mints and burns it, dex pairs it with ERTH). ANML held by a
//     person is always a note.
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	return to, k.checkSend(ctx, from, to, amt)
}

func (k Keeper) checkSend(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	if to.Equals(k.poolAddr) && !from.Equals(k.poolAddr) && !isPoolDeposit(ctx) {
		return types.ErrSendRestricted.Wrap("the shielded pool accepts coins only through MsgShield or MintNote")
	}
	if k.shieldedOnlyTo[string(to)] {
		return nil
	}
	for _, c := range amt {
		if k.shieldedOnly[c.Denom] {
			return types.ErrSendRestricted.Wrapf("%s exists only in the shielded pool", c.Denom)
		}
	}
	return nil
}

// checkUnshield refuses, before the ante spends anything, an unshield the
// bank would refuse afterwards.
func (k Keeper) checkUnshield(ctx context.Context, coin sdk.Coin, receiver sdk.AccAddress) error {
	if k.bankKeeper.BlockedAddr(receiver) {
		return types.ErrSendRestricted.Wrap("receiver may not receive funds")
	}
	if err := k.bankKeeper.IsSendEnabledCoins(ctx, coin); err != nil {
		return err
	}
	return k.checkSend(ctx, k.poolAddr, receiver, sdk.NewCoins(coin))
}
