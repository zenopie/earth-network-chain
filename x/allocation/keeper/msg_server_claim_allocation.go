package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// ClaimAllocation pays out an ADDRESS option's accrued ERTH to its recipient.
// The coins already exist — the stream minted them as they accrued — so this
// moves them rather than issuing them.
//
// The signer must be the option's claimer, or anyone if the option has no
// claimer set. INTEGRATED options resolve in BeginBlock and cannot be claimed
// this way.
func (k msgServer) ClaimAllocation(ctx context.Context, msg *types.MsgClaimAllocation) (*types.MsgClaimAllocationResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	if _, err := k.addressCodec.StringToBytes(msg.Creator); err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}

	opt, err := k.Options.Get(ctx, optionKey(msg.Stream, msg.OptionId))
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrOptionNotFound, "option %d", msg.OptionId)
	}
	if opt.Kind != types.ALLOCATION_KIND_ADDRESS {
		return nil, errorsmod.Wrap(types.ErrNotClaimable, "only ADDRESS options are claimable")
	}
	// A claimer, when set, restricts who may trigger the claim; when empty anyone
	// may. Either way the payout goes to opt.Recipient, so a permissionless
	// trigger cannot redirect funds — it only spends the triggerer's gas.
	if opt.Claimer != "" && msg.Creator != opt.Claimer {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "only the option claimer can claim")
	}

	if err := k.AdvanceIndex(ctx, msg.Stream); err != nil {
		return nil, err
	}
	rewardIndex, err := k.getRewardIndex(ctx, msg.Stream)
	if err != nil {
		return nil, err
	}
	settleOption(&opt, rewardIndex)

	amount := opt.Accumulated
	if amount.IsPositive() {
		recipientBz, err := k.addressCodec.StringToBytes(opt.Recipient)
		if err != nil {
			return nil, err
		}
		if err := k.PayOut(ctx, sdk.AccAddress(recipientBz), amount); err != nil {
			return nil, err
		}
		opt.Accumulated = math.ZeroInt()
	}
	if err := k.setOption(ctx, msg.Stream, opt); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"claim_allocation",
			sdk.NewAttribute("stream", msg.Stream.String()),
			sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
			sdk.NewAttribute("recipient", opt.Recipient),
			sdk.NewAttribute("amount", amount.String()),
		),
	)

	return &types.MsgClaimAllocationResponse{}, nil
}
