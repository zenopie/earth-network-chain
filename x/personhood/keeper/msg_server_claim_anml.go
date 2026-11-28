package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// ClaimAnml is retired (v1.4.0): its private action refuses it in the ante,
// so this never runs; any path that reaches it is refused the same way.
func (k msgServer) ClaimAnml(goCtx context.Context, msg *types.MsgClaimAnml) (*types.MsgClaimAnmlResponse, error) {
	// Off the private ante (a contract, an ICA host) it is refused as every
	// private msg is; through it, the ante already refused it.
	if _, _, err := authorized[MembershipStatement](goCtx, msg); err != nil {
		return nil, err
	}
	return nil, types.ErrClaimRetired
}

// emitClaim is a day's claim_anml event.
func emitClaim(ctx sdk.Context, day uint64, nullifier []byte, pos uint64) {
	ctx.EventManager().EmitEvent(sdk.NewEvent("claim_anml",
		sdk.NewAttribute("day", strconv.FormatUint(day, 10)),
		sdk.NewAttribute("nullifier", hexOf(nullifier)),
		sdk.NewAttribute("position", strconv.FormatUint(pos, 10)),
	))
}

// ClaimAnmlDays mints 1 ANML per day for the run first_day..today, as one
// note, recording each day's nullifier. The person is never named: the claim
// proof shows a live identity leaf, and its nullifier for scope claim/day is
// what a second claim of that day would repeat. The ante (checkClaimDays)
// refused any claimed day just before, in this tx.
func (k msgServer) ClaimAnmlDays(goCtx context.Context, msg *types.MsgClaimAnmlDays) (*types.MsgClaimAnmlDaysResponse, error) {
	ctx, _, err := authorized[ClaimDaysStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	for i, nf := range msg.Claim.Nullifiers {
		if err := k.ClaimNullifiers.Set(ctx, collections.Join(msg.FirstDay+uint64(i), nf)); err != nil {
			return nil, err
		}
	}
	pos, err := k.mintAnmlNoteOf(ctx, msg.Days(), msg.Pc, msg.Ciphertext)
	if err != nil {
		return nil, err
	}
	// One claim_anml event per day, as a MsgClaimAnml for each would emit,
	// all at the one note's position.
	for i, nf := range msg.Claim.Nullifiers {
		emitClaim(ctx, msg.FirstDay+uint64(i), nf, pos)
	}
	return &types.MsgClaimAnmlDaysResponse{Position: pos}, nil
}
