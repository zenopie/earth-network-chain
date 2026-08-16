package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// RemoveLiquidity begins a withdrawal. It does not pay out.
//
// The shares are escrowed on the module account for the unbonding period and the
// assets are swept to the provider's wallet when it matures, with no second
// transaction to submit. Because the shares stay outstanding while they wait,
// the pool keeps the depth for the whole period and the departing provider keeps
// both their share of LP rewards and their exposure to the pool — so the payout
// is priced at maturity rather than here.
func (k msgServer) RemoveLiquidity(ctx context.Context, msg *types.MsgRemoveLiquidity) (*types.MsgRemoveLiquidityResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	if _, err := k.Pool.Get(ctx, msg.PoolId); err != nil {
		return nil, errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", msg.PoolId)
	}
	if msg.Shares.Denom != types.LPShareDenom(msg.PoolId) {
		return nil, errorsmod.Wrapf(types.ErrInvalidDenom, "expected LP denom %s", types.LPShareDenom(msg.PoolId))
	}
	if !msg.Shares.Amount.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "shares must be positive")
	}

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	// Escrowing before recording means a provider who does not hold the shares is
	// rejected by the bank here, rather than leaving a claim on liquidity they
	// never had.
	if err := k.escrowShares(ctx, creator, msg.Shares); err != nil {
		return nil, err
	}

	completion := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + int64(params.LpUnbondingSeconds)
	key := collections.Join3(completion, msg.PoolId, creatorBz)

	// Two withdrawals in the same block land on the same key, so fold them
	// together instead of letting the second overwrite the first.
	entry, err := k.LpUnbondings.Get(ctx, key)
	switch {
	case err == nil:
		entry.Shares = entry.Shares.Add(msg.Shares)
	case errors.Is(err, collections.ErrNotFound):
		entry = types.LpUnbonding{
			Address:        msg.Creator,
			PoolId:         msg.PoolId,
			Shares:         msg.Shares,
			CompletionTime: completion,
		}
	default:
		return nil, err
	}
	if err := k.LpUnbondings.Set(ctx, key, entry); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"begin_unbond_liquidity",
			sdk.NewAttribute("pool_id", strconv.FormatUint(msg.PoolId, 10)),
			sdk.NewAttribute("provider", msg.Creator),
			sdk.NewAttribute("shares", msg.Shares.String()),
			sdk.NewAttribute("completion_time", strconv.FormatInt(completion, 10)),
		),
	)

	return &types.MsgRemoveLiquidityResponse{CompletionTime: completion}, nil
}
