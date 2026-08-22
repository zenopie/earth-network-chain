package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// Swap trades token_in for denom_out, routing through the ERTH hub:
//   - ERTH  -> token : a single hop into the token's pool.
//   - token -> ERTH  : a single hop out of the token's pool.
//   - tokenA -> tokenB : two hops, tokenA -> ERTH -> tokenB.
//
// Each hop charges the governance swap fee (denominated in ERTH); half stays in
// the pool for LPs and half is burned. Slippage is enforced on the final output.
func (k msgServer) Swap(ctx context.Context, msg *types.MsgSwap) (*types.MsgSwapResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	minOut, ok := math.NewIntFromString(msg.MinAmountOut)
	if !ok || minOut.IsNegative() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "invalid min_amount_out")
	}

	tokenOut, err := k.SwapExactIn(ctx, sdk.AccAddress(creatorBz), msg.TokenIn, msg.DenomOut, minOut)
	if err != nil {
		return nil, err
	}
	return &types.MsgSwapResponse{TokenOut: tokenOut}, nil
}

// swapParty is whoever the dex is trading for: an ordinary account, or another
// module.
//
// The distinction exists because the two cannot be paid the same way. Module
// accounts are on the bank's blocked list (blockAccAddrs, app/app_config.go) and
// SendCoinsFromModuleToAccount honours that list, so paying a module through the
// account path fails with "not allowed to receive funds". Module-to-module
// transfers are not subject to it, which is the correct primitive for one module
// trading against another.
type swapParty struct {
	addr   sdk.AccAddress // set for an ordinary trader
	module string         // set when the counterparty is a module account
}

func (p swapParty) escrow(ctx context.Context, k Keeper, amt sdk.Coins) error {
	if p.module != "" {
		return k.bankKeeper.SendCoinsFromModuleToModule(ctx, p.module, types.ModuleName, amt)
	}
	return k.bankKeeper.SendCoinsFromAccountToModule(ctx, p.addr, types.ModuleName, amt)
}

func (p swapParty) pay(ctx context.Context, k Keeper, amt sdk.Coins) error {
	if p.module != "" {
		return k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, p.module, amt)
	}
	return k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, p.addr, amt)
}

func (p swapParty) String() string {
	if p.module != "" {
		return p.module
	}
	return p.addr.String()
}

// SwapExactIn swaps tokenIn for denomOut on behalf of an ordinary account.
func (k Keeper) SwapExactIn(ctx context.Context, trader sdk.AccAddress, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error) {
	return k.swapExactIn(ctx, swapParty{addr: trader}, tokenIn, denomOut, minOut)
}

// SwapExactInForModule is SwapExactIn for another module trading against the
// dex — the x/personhood ANML buyback being the one that matters.
//
// It exists because the buyback silently did nothing without it. The payout ran
// through SendCoinsFromModuleToAccount into a module account the bank blocks
// from receiving funds, so every buyback failed with "unauthorized" and was
// discarded by design, on every block, without an event. The pillar looked like
// it was running and was emitting nothing.
func (k Keeper) SwapExactInForModule(ctx context.Context, moduleName string, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error) {
	return k.swapExactIn(ctx, swapParty{module: moduleName}, tokenIn, denomOut, minOut)
}

// swapExactIn routes tokenIn for denomOut through the ERTH hub (1 or 2 hops),
// charging the per-hop fee/burn and enforcing minOut.
func (k Keeper) swapExactIn(ctx context.Context, trader swapParty, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error) {
	if !tokenIn.Amount.IsPositive() {
		return sdk.Coin{}, errorsmod.Wrap(types.ErrInvalidAmount, "token_in must be positive")
	}
	if denomOut == tokenIn.Denom {
		return sdk.Coin{}, errorsmod.Wrap(types.ErrInvalidDenom, "token_in and denom_out must differ")
	}

	hub, err := k.HubDenom(ctx)
	if err != nil {
		return sdk.Coin{}, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return sdk.Coin{}, err
	}
	fee := params.SwapFee

	// Escrow the input.
	if err := trader.escrow(ctx, k, sdk.NewCoins(tokenIn)); err != nil {
		return sdk.Coin{}, err
	}

	var (
		outAmt    math.Int
		totalBurn = math.ZeroInt()
	)

	switch {
	case tokenIn.Denom == hub:
		// ERTH -> token
		out, burn, err := k.hopHubToToken(ctx, denomOut, tokenIn.Amount, fee)
		if err != nil {
			return sdk.Coin{}, err
		}
		outAmt, totalBurn = out, burn

	case denomOut == hub:
		// token -> ERTH
		out, burn, err := k.hopTokenToHub(ctx, tokenIn.Denom, tokenIn.Amount, fee)
		if err != nil {
			return sdk.Coin{}, err
		}
		outAmt, totalBurn = out, burn

	default:
		// tokenA -> ERTH -> tokenB
		erthMid, burn1, err := k.hopTokenToHub(ctx, tokenIn.Denom, tokenIn.Amount, fee)
		if err != nil {
			return sdk.Coin{}, err
		}
		out, burn2, err := k.hopHubToToken(ctx, denomOut, erthMid, fee)
		if err != nil {
			return sdk.Coin{}, err
		}
		outAmt = out
		totalBurn = burn1.Add(burn2)
	}

	if !outAmt.IsPositive() {
		return sdk.Coin{}, errorsmod.Wrap(types.ErrInsufficientPool, "output rounds to zero")
	}
	if outAmt.LT(minOut) {
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrSlippage, "got %s, want >= %s", outAmt, minOut)
	}

	tokenOut := sdk.NewCoin(denomOut, outAmt)
	if err := trader.pay(ctx, k, sdk.NewCoins(tokenOut)); err != nil {
		return sdk.Coin{}, err
	}
	if totalBurn.IsPositive() {
		if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(sdk.NewCoin(hub, totalBurn))); err != nil {
			return sdk.Coin{}, err
		}
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"swap",
			sdk.NewAttribute("trader", trader.String()),
			sdk.NewAttribute("token_in", tokenIn.String()),
			sdk.NewAttribute("token_out", tokenOut.String()),
			sdk.NewAttribute("erth_burned", totalBurn.String()),
		),
	)

	return tokenOut, nil
}

// hopTokenToHub executes one spoke-token -> ERTH hop against the token's pool and
// persists the updated reserves. It returns the net ERTH out and the ERTH burned.
func (k Keeper) hopTokenToHub(ctx context.Context, tokenDenom string, amountIn math.Int, swapFee math.LegacyDec) (out, burn math.Int, err error) {
	pool, err := k.PoolForToken(ctx, tokenDenom)
	if err != nil {
		return math.Int{}, math.Int{}, errorsmod.Wrapf(types.ErrPoolNotFound, "no pool for %s", tokenDenom)
	}
	// The price accumulator is advanced inside settlePoolRewards below, which
	// runs before the swap maths, so the interval that just elapsed is booked at
	// the pre-trade price without this path having to remember to do it.
	// Compound pending LP rewards into the reserve before pricing against it.
	if err := k.settlePoolRewards(ctx, pool.PoolId, &pool); err != nil {
		return math.Int{}, math.Int{}, err
	}
	r := swapTokenForHub(pool.ReserveErth.Amount, pool.ReserveToken.Amount, amountIn, swapFee)
	if !r.amountOut.IsPositive() || !r.newReserveErth.IsPositive() {
		return math.Int{}, math.Int{}, errorsmod.Wrap(types.ErrInsufficientPool, "swap would drain the pool")
	}
	pool.ReserveErth.Amount = r.newReserveErth
	pool.ReserveToken.Amount = r.newReserveToken
	if err := k.applyVolume(ctx, &pool, r.volumeErth, sdk.UnwrapSDKContext(ctx).BlockTime()); err != nil {
		return math.Int{}, math.Int{}, err
	}
	if err := k.SetPool(ctx, pool.PoolId, pool); err != nil {
		return math.Int{}, math.Int{}, err
	}
	return r.amountOut, r.burnErth, nil
}

// hopHubToToken executes one ERTH -> spoke-token hop against the token's pool and
// persists the updated reserves. It returns the net token out and the ERTH burned.
func (k Keeper) hopHubToToken(ctx context.Context, tokenDenom string, amountErthIn math.Int, swapFee math.LegacyDec) (out, burn math.Int, err error) {
	pool, err := k.PoolForToken(ctx, tokenDenom)
	if err != nil {
		return math.Int{}, math.Int{}, errorsmod.Wrapf(types.ErrPoolNotFound, "no pool for %s", tokenDenom)
	}
	// The price accumulator is advanced inside settlePoolRewards below, which
	// runs before the swap maths, so the interval that just elapsed is booked at
	// the pre-trade price without this path having to remember to do it.
	// Compound pending LP rewards into the reserve before pricing against it.
	if err := k.settlePoolRewards(ctx, pool.PoolId, &pool); err != nil {
		return math.Int{}, math.Int{}, err
	}
	r := swapHubForToken(pool.ReserveErth.Amount, pool.ReserveToken.Amount, amountErthIn, swapFee)
	if !r.amountOut.IsPositive() || !r.newReserveToken.IsPositive() {
		return math.Int{}, math.Int{}, errorsmod.Wrap(types.ErrInsufficientPool, "swap would drain the pool")
	}
	pool.ReserveErth.Amount = r.newReserveErth
	pool.ReserveToken.Amount = r.newReserveToken
	if err := k.applyVolume(ctx, &pool, r.volumeErth, sdk.UnwrapSDKContext(ctx).BlockTime()); err != nil {
		return math.Int{}, math.Int{}, err
	}
	if err := k.SetPool(ctx, pool.PoolId, pool); err != nil {
		return math.Int{}, math.Int{}, err
	}
	return r.amountOut, r.burnErth, nil
}
