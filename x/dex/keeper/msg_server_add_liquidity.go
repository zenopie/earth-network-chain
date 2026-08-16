package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// AddLiquidity deposits ERTH and the pool's spoke token and mints LP shares
// proportional to the contribution. Deposits are taken in the pool ratio; any
// excess implied by the provided amounts is simply not pulled from the sender.
//
// The two amounts may be supplied in either order; they are matched to the pool
// reserves by denom.
func (k msgServer) AddLiquidity(ctx context.Context, msg *types.MsgAddLiquidity) (*types.MsgAddLiquidityResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	pool, err := k.Pool.Get(ctx, msg.PoolId)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", msg.PoolId)
	}
	// Settle pending LP rewards into the reserve before minting shares against
	// it, so a depositor cannot buy in ahead of rewards earned before they came.
	if err := k.settlePoolRewards(ctx, msg.PoolId, &pool); err != nil {
		return nil, err
	}

	// Match the two provided coins to the pool's erth/token reserves by denom.
	erthIn, tokenIn, err := matchPair(msg.AmountA, msg.AmountB, pool.ReserveErth.Denom, pool.ReserveToken.Denom)
	if err != nil {
		return nil, err
	}
	if !erthIn.Amount.IsPositive() || !tokenIn.Amount.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "both amounts must be positive")
	}

	total := k.totalShares(ctx, msg.PoolId).Amount

	var (
		shareAmt   math.Int
		depositErt = erthIn
		depositTok = tokenIn
	)

	if total.IsZero() {
		// Pool has no outstanding shares (e.g. fully drained): re-seed it.
		shareAmt = initialShares(erthIn.Amount, tokenIn.Amount)
	} else {
		sharesFromErth := erthIn.Amount.Mul(total).Quo(pool.ReserveErth.Amount)
		sharesFromToken := tokenIn.Amount.Mul(total).Quo(pool.ReserveToken.Amount)
		shareAmt = math.MinInt(sharesFromErth, sharesFromToken)
		if !shareAmt.IsPositive() {
			return nil, types.ErrZeroShares
		}
		// Pull assets in the exact pool ratio for the shares granted.
		depositErt.Amount = shareAmt.Mul(pool.ReserveErth.Amount).Quo(total)
		depositTok.Amount = shareAmt.Mul(pool.ReserveToken.Amount).Quo(total)
	}

	if !shareAmt.IsPositive() {
		return nil, types.ErrZeroShares
	}

	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, creator, types.ModuleName, sdk.NewCoins(depositErt, depositTok)); err != nil {
		return nil, err
	}

	shares := sdk.NewCoin(types.LPShareDenom(msg.PoolId), shareAmt)
	if err := k.mintShares(ctx, creator, shares); err != nil {
		return nil, err
	}

	pool.ReserveErth = pool.ReserveErth.Add(depositErt)
	pool.ReserveToken = pool.ReserveToken.Add(depositTok)
	if err := k.Pool.Set(ctx, msg.PoolId, pool); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"add_liquidity",
			sdk.NewAttribute("pool_id", strconv.FormatUint(msg.PoolId, 10)),
			sdk.NewAttribute("provider", msg.Creator),
			sdk.NewAttribute("shares", shares.String()),
		),
	)

	return &types.MsgAddLiquidityResponse{Shares: shares}, nil
}

// matchPair assigns two coins to the (erth, token) slots by denom, in whichever
// order they were provided.
func matchPair(a, b sdk.Coin, erthDenom, tokenDenom string) (erth, token sdk.Coin, err error) {
	switch {
	case a.Denom == erthDenom && b.Denom == tokenDenom:
		return a, b, nil
	case a.Denom == tokenDenom && b.Denom == erthDenom:
		return b, a, nil
	default:
		return sdk.Coin{}, sdk.Coin{}, errorsmod.Wrapf(types.ErrInvalidDenom, "expected %s and %s", erthDenom, tokenDenom)
	}
}
