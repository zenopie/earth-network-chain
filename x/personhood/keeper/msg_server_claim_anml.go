package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// ClaimAnml mints 1 ANML to a registered human, at most once per UTC day.
func (k msgServer) ClaimAnml(ctx context.Context, msg *types.MsgClaimAnml) (*types.MsgClaimAnmlResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	reg, err := k.requireValidHuman(ctx, creator)
	if err != nil {
		return nil, err
	}

	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	if now-reg.LastAnmlClaim < 86400 {
		return nil, types.ErrClaimTooSoon
	}
	reg.LastAnmlClaim = (now / 86400) * 86400
	if err := k.Registrations.Set(ctx, reg.Nullifier, reg); err != nil {
		return nil, err
	}

	anml := sdk.NewCoins(sdk.NewInt64Coin(types.AnmlDenom, types.OneAnml))
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, anml); err != nil {
		return nil, err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, creator, anml); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"claim_anml",
			sdk.NewAttribute("address", msg.Creator),
			sdk.NewAttribute("amount", anml.String()),
		),
	)

	return &types.MsgClaimAnmlResponse{}, nil
}
