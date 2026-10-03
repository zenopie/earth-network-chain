package keeper

import (
	"context"
	earthtypes "github.com/earth-network/earth/x/earth/types"
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
//
// A pool whose token is shielded-only (ANML/ERTH) is refused: no account
// holds ANML, so that pool takes deposits only from notes
// (MsgAddLiquidityShielded).
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
	if err := k.refuseShieldedOnly(pool.ReserveToken.Denom, msg.AmountA.Denom, msg.AmountB.Denom); err != nil {
		return nil, err
	}
	// Match the two provided coins to the pool's erth/token reserves by denom.
	erthIn, tokenIn, err := matchPair(msg.AmountA, msg.AmountB, pool.ReserveErth.Denom, pool.ReserveToken.Denom)
	if err != nil {
		return nil, err
	}
	shares, _, _, err := k.deposit(ctx, msg.PoolId, erthIn, tokenIn, msg.MinShares, creator,
		func(depErt, depTok sdk.Coin) error {
			return k.bankKeeper.SendCoinsFromAccountToModule(ctx, creator, types.ModuleName, sdk.NewCoins(depErt, depTok))
		})
	if err != nil {
		return nil, err
	}
	return &types.MsgAddLiquidityResponse{Shares: shares}, nil
}

// deposit adds up to erthIn and tokenIn to pool poolID, taken in the pool
// ratio, and mints the LP shares to provider (nil: onto this module's
// account, for a private deposit to mint as a note; its event then names no
// provider). pull moves the deposit (exactly
// the amounts the ratio takes, which deposit returns) into this module's
// account; whatever of erthIn and tokenIn it does not take is the caller's to
// return (MsgAddLiquidity simply never pulls it).
func (k Keeper) deposit(ctx context.Context, poolID uint64, erthIn, tokenIn sdk.Coin, minSharesStr string,
	provider sdk.AccAddress, pull func(depErt, depTok sdk.Coin) error,
) (sdk.Coin, sdk.Coin, sdk.Coin, error) {
	var none sdk.Coin
	pool, err := k.Pool.Get(ctx, poolID)
	if err != nil {
		return none, none, none, errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", poolID)
	}
	// Settle pending LP rewards into the reserve before minting shares against
	// it, so a depositor cannot buy in ahead of rewards earned before they came.
	if err := k.settlePoolRewards(ctx, poolID, &pool); err != nil {
		return none, none, none, err
	}
	if erthIn.Denom != pool.ReserveErth.Denom || tokenIn.Denom != pool.ReserveToken.Denom {
		return none, none, none, errorsmod.Wrapf(types.ErrInvalidDenom, "expected %s and %s", pool.ReserveErth.Denom, pool.ReserveToken.Denom)
	}
	if !erthIn.Amount.IsPositive() || !tokenIn.Amount.IsPositive() {
		return none, none, none, errorsmod.Wrap(types.ErrInvalidAmount, "both amounts must be positive")
	}

	// The pool cap, before any arithmetic: each input, and below the
	// resulting reserves and share supply (see types.MaxPoolAmount).
	if !types.WithinPoolCap(erthIn.Amount) || !types.WithinPoolCap(tokenIn.Amount) {
		return none, none, none, errorsmod.Wrapf(types.ErrPoolCap, "each amount must be at most %s", types.MaxPoolAmount)
	}

	total := k.totalShares(ctx, poolID).Amount
	if !types.WithinPoolCap(total) {
		return none, none, none, errorsmod.Wrapf(types.ErrPoolCap, "pool %d share supply %s", poolID, total)
	}

	var (
		shareAmt   math.Int
		depositErt = erthIn
		depositTok = tokenIn
	)

	if total.IsZero() {
		// Pool has no outstanding shares (e.g. fully drained): re-seed it.
		//
		// Whatever the reserves still hold goes first. With no shares it
		// belongs to no one — rewards settled in after the last provider left,
		// rounding from withdrawals — and seeding on top of it minted the
		// depositor shares over all of it, so the next person to deposit into
		// an empty pool could withdraw straight away with the residue.
		if err := k.burnResidue(ctx, &pool); err != nil {
			return none, none, none, err
		}
		shareAmt = initialShares(erthIn.Amount, tokenIn.Amount)
	} else {
		// A pool with shares outstanding but a zero reserve (drained by
		// rounding, or 0/0 after the last withdrawal) cannot price a deposit.
		if !pool.ReserveErth.Amount.IsPositive() || !pool.ReserveToken.Amount.IsPositive() {
			return none, none, none, errorsmod.Wrapf(types.ErrInsufficientPool,
				"pool %d holds %s / %s against %s shares", poolID, pool.ReserveErth, pool.ReserveToken, total)
		}
		sharesFromErth, err := mulDiv(erthIn.Amount, total, pool.ReserveErth.Amount)
		if err != nil {
			return none, none, none, err
		}
		sharesFromToken, err := mulDiv(tokenIn.Amount, total, pool.ReserveToken.Amount)
		if err != nil {
			return none, none, none, err
		}
		shareAmt = math.MinInt(sharesFromErth, sharesFromToken)
		if !shareAmt.IsPositive() {
			return none, none, none, types.ErrZeroShares
		}
		// Pull assets in the exact pool ratio for the shares granted, each
		// leg rounded UP, in the existing providers' favour. Flooring them
		// minted a depositor shares worth up to one unit more of each leg
		// than they paid; when a share is worth less than one unit of a leg
		// that is nearly twice the leg (audit 4, C2). The ceiling never
		// exceeds what was offered: shareAmt <= in*total/reserve, so
		// shareAmt*reserve/total <= in, and in is an integer.
		if depositErt.Amount, err = mulDivUp(shareAmt, pool.ReserveErth.Amount, total); err != nil {
			return none, none, none, err
		}
		if depositTok.Amount, err = mulDivUp(shareAmt, pool.ReserveToken.Amount, total); err != nil {
			return none, none, none, err
		}
		if !depositErt.Amount.IsPositive() || !depositTok.Amount.IsPositive() ||
			depositErt.Amount.GT(erthIn.Amount) || depositTok.Amount.GT(tokenIn.Amount) {
			return none, none, none, errorsmod.Wrapf(types.ErrZeroShares,
				"deposit would pull %s and %s of %s and %s offered", depositErt, depositTok, erthIn, tokenIn)
		}
	}
	if !types.WithinPoolCap(total.Add(shareAmt)) {
		return none, none, none, errorsmod.Wrapf(types.ErrPoolCap,
			"pool %d share supply would pass %s", poolID, types.MaxPoolAmount)
	}
	if !types.WithinPoolCap(pool.ReserveErth.Amount.Add(depositErt.Amount)) ||
		!types.WithinPoolCap(pool.ReserveToken.Amount.Add(depositTok.Amount)) {
		return none, none, none, errorsmod.Wrapf(types.ErrPoolCap,
			"pool %d reserves would pass %s", poolID, types.MaxPoolAmount)
	}

	if !shareAmt.IsPositive() {
		return none, none, none, types.ErrZeroShares
	}

	// Slippage. The shares above were priced against the reserves as they stand
	// at execution, which is not the ratio the depositor saw when they signed:
	// a trade landing in between moves it, and moving it deliberately either
	// side of this message is the standard sandwich. Checked after the amount is
	// known and before any coins move, so a deposit that would not have been
	// worth making costs its sender the gas and nothing else.
	//
	// An empty min_shares is no minimum. Clients built before the field existed
	// send nothing and keep working.
	if minSharesStr != "" {
		minShares, ok := math.NewIntFromString(minSharesStr)
		if !ok || minShares.IsNegative() {
			return none, none, none, errorsmod.Wrap(types.ErrInvalidAmount, "invalid min_shares")
		}
		if shareAmt.LT(minShares) {
			return none, none, none, errorsmod.Wrapf(types.ErrSlippage,
				"would mint %s shares, want >= %s", shareAmt, minShares)
		}
	}

	if err := pull(depositErt, depositTok); err != nil {
		return none, none, none, err
	}

	shares := sdk.NewCoin(types.LPShareDenom(poolID), shareAmt)
	if provider == nil {
		if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, sdk.NewCoins(shares)); err != nil {
			return none, none, none, err
		}
	} else if err := k.mintShares(ctx, provider, shares); err != nil {
		return none, none, none, err
	}

	pool.ReserveErth = pool.ReserveErth.Add(depositErt)
	pool.ReserveToken = pool.ReserveToken.Add(depositTok)
	if err := k.SetPool(ctx, poolID, pool); err != nil {
		return none, none, none, err
	}

	attrs := []sdk.Attribute{sdk.NewAttribute("pool_id", strconv.FormatUint(poolID, 10))}
	if provider != nil {
		attrs = append(attrs, sdk.NewAttribute("provider", provider.String()))
	}
	attrs = append(attrs, sdk.NewAttribute("shares", shares.String()))
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent("add_liquidity", attrs...))

	return shares, depositErt, depositTok, nil
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

// burnResidue destroys an empty pool's leftover reserves and zeroes them.
func (k Keeper) burnResidue(ctx context.Context, pool *types.Pool) error {
	residue := sdk.NewCoins(pool.ReserveErth, pool.ReserveToken)
	if residue.IsZero() {
		return nil
	}
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, residue); err != nil {
		return err
	}
	if err := k.burnRecorder.RecordBurn(ctx, earthtypes.SourceDexResidue, residue); err != nil {
		return err
	}
	pool.ReserveErth.Amount = math.ZeroInt()
	pool.ReserveToken.Amount = math.ZeroInt()
	return nil
}
