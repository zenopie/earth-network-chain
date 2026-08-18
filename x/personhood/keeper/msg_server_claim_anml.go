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

	// One claim per UTC day, not one per rolling 24 hours.
	//
	// The difference matters to whoever is claiming. A rolling window makes
	// each claim set the next one 24 hours later, so claiming at 21:00 pushes
	// tomorrow's to 21:00, and a day claimed late drags every following day
	// later with it — which turns a daily claim into a thing you have to be
	// punctual about to avoid losing days. A calendar boundary has no such
	// drift: claim whenever you like, and the next one opens at midnight.
	//
	// Both sides are compared as day numbers rather than differencing the
	// timestamps. The previous form stored a truncated midnight and then
	// tested `now-last < 86400`, which gives the same answer only because of
	// the truncation — remove that line as redundant and the drift comes back
	// silently.
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	const secondsPerDay = 86400
	if now/secondsPerDay == reg.LastAnmlClaim/secondsPerDay {
		return nil, types.ErrClaimTooSoon
	}
	reg.LastAnmlClaim = now
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
