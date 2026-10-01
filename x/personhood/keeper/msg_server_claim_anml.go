package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// ClaimAnml mints 1 ANML to a note, once per person per UTC day.
//
// The person is never named: the membership proof shows a live identity leaf,
// and its nullifier for scope claim/day is what a second claim that day would
// repeat. One claim per calendar day, not per rolling 24 hours: claim whenever
// you like, and the next opens at midnight UTC.
func (k msgServer) ClaimAnml(goCtx context.Context, msg *types.MsgClaimAnml) (*types.MsgClaimAnmlResponse, error) {
	ctx, _, err := authorized[MembershipStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	key := collections.Join(msg.Day, msg.Membership.Nullifier)
	used, err := k.ClaimNullifiers.Has(ctx, key)
	if err != nil {
		return nil, err
	}
	if used {
		return nil, types.ErrClaimTooSoon
	}
	if err := k.ClaimNullifiers.Set(ctx, key); err != nil {
		return nil, err
	}
	pos, err := k.mintAnmlNote(ctx, msg.Pc, msg.Ciphertext)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("claim_anml",
		sdk.NewAttribute("day", strconv.FormatUint(msg.Day, 10)),
		sdk.NewAttribute("nullifier", hexOf(msg.Membership.Nullifier)),
		sdk.NewAttribute("position", strconv.FormatUint(pos, 10)),
	))
	return &types.MsgClaimAnmlResponse{Position: pos}, nil
}
