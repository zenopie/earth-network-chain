package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// CreatePool creates a new spoke-and-wheel pool. Exactly one of the two provided
// assets must be the hub asset (ERTH); the other is the spoke token. Only one
// pool may exist per spoke token. The initial LP shares (sqrt(erth*token)) are
// minted to the creator.
//
// Permissionless, but not until the genesis liquidity auction has settled — see
// Keeper.PoolCreationLocked for why the auction cannot defend its own bid denom.
func (k msgServer) CreatePool(ctx context.Context, msg *types.MsgCreatePool) (*types.MsgCreatePoolResponse, error) {
	// Checked before anything else: this is a "not yet", not a judgement about
	// the message, and it should read that way in the error.
	if locked, err := k.PoolCreationLocked(ctx); err != nil {
		return nil, err
	} else if locked {
		return nil, types.ErrPoolCreationLocked
	}

	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	if msg.AmountA.Denom == msg.AmountB.Denom {
		return nil, types.ErrSameDenom
	}
	if !msg.AmountA.IsValid() || !msg.AmountA.Amount.IsPositive() ||
		!msg.AmountB.IsValid() || !msg.AmountB.Amount.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "both amounts must be positive")
	}

	hub, err := k.HubDenom(ctx)
	if err != nil {
		return nil, err
	}

	// Exactly one side must be the hub asset (ERTH).
	var erth, token sdk.Coin
	switch hub {
	case msg.AmountA.Denom:
		erth, token = msg.AmountA, msg.AmountB
	case msg.AmountB.Denom:
		erth, token = msg.AmountB, msg.AmountA
	default:
		return nil, errorsmod.Wrapf(types.ErrInvalidDenom, "one side must be the hub asset %s", hub)
	}

	if has, err := k.PoolByToken.Has(ctx, token.Denom); err != nil {
		return nil, err
	} else if has {
		return nil, errorsmod.Wrapf(types.ErrPoolExists, "pool for %s already exists", token.Denom)
	}

	poolID, err := k.PoolSeq.Next(ctx)
	if err != nil {
		return nil, err
	}

	// Escrow the deposited assets in the module account.
	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, creator, types.ModuleName, sdk.NewCoins(erth, token)); err != nil {
		return nil, err
	}

	shareAmt := initialShares(erth.Amount, token.Amount)
	if !shareAmt.IsPositive() {
		return nil, types.ErrZeroShares
	}
	shares := sdk.NewCoin(types.LPShareDenom(poolID), shareAmt)
	if err := k.mintShares(ctx, creator, shares); err != nil {
		return nil, err
	}

	pool := types.Pool{
		PoolId:        poolID,
		ReserveErth:   erth,
		ReserveToken:  token,
		Volume:        math.ZeroInt(),
		LastVolumeDay: uint64(sdk.UnwrapSDKContext(ctx).BlockTime().Unix()) / 86400,
	}
	if err := k.SetPool(ctx, poolID, pool); err != nil {
		return nil, err
	}
	if err := k.PoolByToken.Set(ctx, token.Denom, poolID); err != nil {
		return nil, err
	}
	if err := k.initPoolLpIndex(ctx, poolID); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"create_pool",
			sdk.NewAttribute("pool_id", strconv.FormatUint(poolID, 10)),
			sdk.NewAttribute("creator", msg.Creator),
			sdk.NewAttribute("token", token.Denom),
			sdk.NewAttribute("shares", shares.String()),
		),
	)

	return &types.MsgCreatePoolResponse{PoolId: poolID}, nil
}
