package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// BeginBlocker retires lapsed registrations and runs the ANML
// buyback-and-burn (1 ERTH/sec).
//
// It must run before x/allocation's BeginBlocker: the sweep returns a lapsed
// human's vote weight to the human stream, and doing that first is what makes
// this block's emission split across live humans only.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	if err := k.sweepExpiredRegistrations(ctx); err != nil {
		return err
	}

	return k.buybackAndBurn(ctx)
}

func (k Keeper) getLastBuyback(ctx context.Context) (int64, error) {
	v, err := k.LastBuyback.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

func (k Keeper) moduleAddress() sdk.AccAddress {
	return authtypes.NewModuleAddress(types.ModuleName)
}

// buybackAndBurn mints ERTH prorated by block time (1 ERTH/sec), swaps it for
// ANML on the dex, and burns the ANML. The mint→swap→burn is done atomically in
// a cache context so a missing pool or a rounding failure can never halt the
// block or leak funds.
func (k Keeper) buybackAndBurn(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime().UnixNano()

	last, err := k.getLastBuyback(ctx)
	if err != nil {
		return err
	}
	// Always advance the clock; skipped emission is simply not minted.
	if err := k.LastBuyback.Set(ctx, now); err != nil {
		return err
	}
	if last == 0 || now <= last {
		return nil
	}

	amount := math.NewInt(types.EmissionPerSecond).MulRaw(now - last).QuoRaw(int64(time.Second))
	if !amount.IsPositive() {
		return nil
	}

	has, err := k.dexKeeper.HasPoolForToken(ctx, types.AnmlDenom)
	if err != nil {
		return err
	}
	if !has {
		return nil // no ANML pool yet — carry nothing, wait for one
	}
	erth, err := k.erthDenom(ctx)
	if err != nil {
		return err
	}
	if erth == types.AnmlDenom {
		return nil
	}

	cacheCtx, write := sdkCtx.CacheContext()
	erthIn := sdk.NewCoin(erth, amount)
	if err := k.bankKeeper.MintCoins(cacheCtx, types.ModuleName, sdk.NewCoins(erthIn)); err != nil {
		return nil // discard
	}
	out, err := k.dexKeeper.SwapExactIn(cacheCtx, k.moduleAddress(), erthIn, types.AnmlDenom, math.ZeroInt())
	if err != nil {
		return nil // discard: no write
	}
	if out.Amount.IsPositive() {
		if err := k.bankKeeper.BurnCoins(cacheCtx, types.ModuleName, sdk.NewCoins(out)); err != nil {
			return nil // discard
		}
	}
	write()

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			"anml_buyback_burn",
			sdk.NewAttribute("erth_spent", erthIn.String()),
			sdk.NewAttribute("anml_burned", out.String()),
		),
	)
	return nil
}
