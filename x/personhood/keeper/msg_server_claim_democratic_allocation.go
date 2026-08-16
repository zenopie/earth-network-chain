package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// ClaimDemocraticAllocation mints and pays out an ADDRESS option's accrued ERTH
// to its recipient. The signer must be the option's claimer, or anyone if the
// option has no claimer set.
func (k msgServer) ClaimDemocraticAllocation(ctx context.Context, msg *types.MsgClaimDemocraticAllocation) (*types.MsgClaimDemocraticAllocationResponse, error) {
	if _, err := k.addressCodec.StringToBytes(msg.Creator); err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}

	opt, err := k.DemOptions.Get(ctx, msg.OptionId)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrOptionNotFound, "option %d", msg.OptionId)
	}
	if opt.Kind != types.DEMOCRATIC_KIND_ADDRESS {
		return nil, errorsmod.Wrap(types.ErrNotClaimable, "only ADDRESS options are claimable")
	}
	// A claimer, when set, restricts who may trigger the claim; when empty anyone
	// may. Either way the payout goes to opt.Recipient, so a permissionless
	// trigger cannot redirect funds — it only spends the triggerer's gas.
	if opt.Claimer != "" && msg.Creator != opt.Claimer {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "only the option claimer can claim")
	}

	if err := k.advanceDemIndex(ctx); err != nil {
		return nil, err
	}
	rewardIndex, err := k.demRewardIndex(ctx)
	if err != nil {
		return nil, err
	}
	settleOption(&opt, rewardIndex)

	amount := opt.Accumulated
	if amount.IsPositive() {
		erth, err := k.erthDenom(ctx)
		if err != nil {
			return nil, err
		}
		coins := sdk.NewCoins(sdk.NewCoin(erth, amount))
		if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, coins); err != nil {
			return nil, err
		}
		recipientBz, err := k.addressCodec.StringToBytes(opt.Recipient)
		if err != nil {
			return nil, err
		}
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(recipientBz), coins); err != nil {
			return nil, err
		}
		opt.Accumulated = math.ZeroInt()
	}
	if err := k.DemOptions.Set(ctx, msg.OptionId, opt); err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"claim_democratic_allocation",
			sdk.NewAttribute("option_id", strconv.FormatUint(msg.OptionId, 10)),
			sdk.NewAttribute("recipient", opt.Recipient),
			sdk.NewAttribute("amount", amount.String()),
		),
	)

	return &types.MsgClaimDemocraticAllocationResponse{}, nil
}
