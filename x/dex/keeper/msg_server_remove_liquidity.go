package keeper

import (
	"bytes"
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

	pool, err := k.Pool.Get(ctx, msg.PoolId)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", msg.PoolId)
	}
	// A shielded-only token (ANML) is paid out as a note to pc, never to the
	// creator's account; any other pool pays both legs to the account.
	if k.isShieldedOnly(pool.ReserveToken.Denom) {
		if err := k.checkNoteOut(ctx, pool.ReserveToken.Denom, msg.Pc, msg.Ciphertext); err != nil {
			return nil, errorsmod.Wrapf(err, "pool %d pays %s as a note: pc", msg.PoolId, pool.ReserveToken.Denom)
		}
	} else if len(msg.Pc) != 0 || len(msg.Ciphertext) != 0 {
		return nil, errorsmod.Wrapf(types.ErrInvalidPrivateMsg, "pool %d pays its token to the account: no pc", msg.PoolId)
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
		// One entry pays one note: a second withdrawal in the block must
		// name the same one.
		if !bytes.Equal(entry.Pc, msg.Pc) || !bytes.Equal(entry.Ciphertext, msg.Ciphertext) {
			return nil, errorsmod.Wrap(types.ErrInvalidPrivateMsg,
				"a withdrawal from this pool is already pending at this completion time with another pc")
		}
		entry.Shares = entry.Shares.Add(msg.Shares)
	case errors.Is(err, collections.ErrNotFound):
		canon, err := k.addressCodec.BytesToString(creatorBz)
		if err != nil {
			return nil, err
		}
		entry = types.LpUnbonding{
			Address:        canon,
			PoolId:         msg.PoolId,
			Shares:         msg.Shares,
			CompletionTime: completion,
			Pc:             msg.Pc,
			Ciphertext:     msg.Ciphertext,
		}
	default:
		return nil, err
	}
	if err := k.setLpUnbonding(ctx, key, entry); err != nil {
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
