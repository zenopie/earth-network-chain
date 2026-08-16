package keeper

import (
	"bytes"
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// AddIntegratedOption adds an INTEGRATED option, resolved every block by a
// registered protocol handler. Governance-gated (signer = module authority).
func (k msgServer) AddIntegratedOption(ctx context.Context, msg *types.MsgAddIntegratedOption) (*types.MsgAddIntegratedOptionResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	authBz, err := k.addressCodec.StringToBytes(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(authBz, k.GetAuthority()) {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "expected authority to add an integrated option")
	}
	// Handlers are registered for one stream. Refusing a cross-stream match is
	// what stops, say, the human stream's registration-reward pool being attached
	// to the capital stream, where nothing would ever pay it out.
	h, ok := k.integratedHandlers[msg.Handler]
	if !ok {
		return nil, types.ErrUnknownHandler.Wrap(msg.Handler)
	}
	if h.stream != msg.Stream {
		return nil, types.ErrUnknownHandler.Wrapf("handler %q belongs to %s, not %s", msg.Handler, h.stream, msg.Stream)
	}

	id, err := k.appendOption(ctx, msg.Stream, types.AllocationOption{
		Description: msg.Description,
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     msg.Handler,
	})
	if err != nil {
		return nil, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"add_integrated_option",
		sdk.NewAttribute("stream", msg.Stream.String()),
		sdk.NewAttribute("option_id", strconv.FormatUint(id, 10)),
		sdk.NewAttribute("handler", msg.Handler),
	))
	return &types.MsgAddIntegratedOptionResponse{Id: id}, nil
}

// AddAddressOption adds a claim-based ADDRESS option. Permissionless: any
// account may add one to either stream by burning params.address_option_fee
// (ERTH).
func (k msgServer) AddAddressOption(ctx context.Context, msg *types.MsgAddAddressOption) (*types.MsgAddAddressOptionResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	subBz, err := k.addressCodec.StringToBytes(msg.Submitter)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid submitter address")
	}
	if _, err := k.addressCodec.StringToBytes(msg.Recipient); err != nil {
		return nil, errorsmod.Wrap(err, "invalid recipient address")
	}
	// An empty claimer is the permissionless default: anyone may trigger the claim.
	if msg.Claimer != "" {
		if _, err := k.addressCodec.StringToBytes(msg.Claimer); err != nil {
			return nil, errorsmod.Wrap(err, "invalid claimer address")
		}
	}

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	if params.AddressOptionFee > 0 {
		denom, err := k.HubDenom(ctx)
		if err != nil {
			return nil, err
		}
		fee := sdk.NewCoins(sdk.NewCoin(denom, math.NewIntFromUint64(params.AddressOptionFee)))
		if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, sdk.AccAddress(subBz), types.ModuleName, fee); err != nil {
			return nil, err
		}
		if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, fee); err != nil {
			return nil, err
		}
	}

	id, err := k.appendOption(ctx, msg.Stream, types.AllocationOption{
		Description: msg.Description,
		Kind:        types.ALLOCATION_KIND_ADDRESS,
		Recipient:   msg.Recipient,
		Claimer:     msg.Claimer,
	})
	if err != nil {
		return nil, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"add_address_option",
		sdk.NewAttribute("stream", msg.Stream.String()),
		sdk.NewAttribute("option_id", strconv.FormatUint(id, 10)),
		sdk.NewAttribute("recipient", msg.Recipient),
		sdk.NewAttribute("claimer", msg.Claimer),
	))
	return &types.MsgAddAddressOptionResponse{Id: id}, nil
}
