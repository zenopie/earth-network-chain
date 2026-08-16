package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// mintShares mints LP share coins and credits them to recipient.
func (k Keeper) mintShares(ctx context.Context, recipient sdk.AccAddress, shares sdk.Coin) error {
	coins := sdk.NewCoins(shares)
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, coins); err != nil {
		return err
	}
	return k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipient, coins)
}

// escrowShares moves LP share coins from owner onto the module account for the
// unbonding period. They are deliberately not burned: leaving them in
// circulation is what keeps totalShares — and so the pool's share pricing —
// unchanged while a withdrawal is in flight, which is why an unbonding provider
// keeps earning and keeps their exposure to the pool.
func (k Keeper) escrowShares(ctx context.Context, owner sdk.AccAddress, shares sdk.Coin) error {
	return k.bankKeeper.SendCoinsFromAccountToModule(ctx, owner, types.ModuleName, sdk.NewCoins(shares))
}

// burnEscrowedShares burns shares already held on the module account, retiring a
// matured unbonding's stake in the pool.
func (k Keeper) burnEscrowedShares(ctx context.Context, shares sdk.Coin) error {
	return k.bankKeeper.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(shares))
}

// totalShares returns the current outstanding LP share supply for a pool.
func (k Keeper) totalShares(ctx context.Context, poolID uint64) sdk.Coin {
	return k.bankKeeper.GetSupply(ctx, types.LPShareDenom(poolID))
}
