package keeper

import (
	"bytes"
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// AddIntegratedOption adds an INTEGRATED option, resolved every block by a
// registered protocol handler. Governance-gated (signer = module authority).
func (k msgServer) AddIntegratedOption(ctx context.Context, msg *types.MsgAddIntegratedOption) (*types.MsgAddIntegratedOptionResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	if err := types.ValidateDescription(msg.Description); err != nil {
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

// AddAddressOption adds a claim-based ADDRESS option. Who may add one depends
// on the stream, because the same permissionless rule has opposite consequences
// on the two axes.
//
// Caretaker: permissionless. Any account may add one by burning
// params.address_option_fee. Weight there is one human, one vote, so the worst
// case — every human lists their own address and votes for it — is an equal
// split among registered humans. That is a dividend, not a capture: proof of
// personhood caps it at one share each, and it is what that constituency chose.
//
// Groundworks: governance-gated. Weight there is bonded stake, and an option
// payable to whoever listed it makes self-voting the dominant strategy — point
// your weight at your own option and you keep everything it draws, against a
// diffuse fraction of anything shared. Played out, every staker lists
// themselves and the stream pays out pro rata to stake: a second staking yield
// that builds none of the infrastructure the fund exists for. The revocability
// that disciplines every other option is no help, since a self-voter is funded
// entirely by their own weight and other voters leaving raises their share
// rather than lowering it. Neither is the fee, which is priced against volume —
// one burn against a perpetual pro-rata claim pays for itself. Requiring the
// authority is what takes the unilateral move away.
func (k msgServer) AddAddressOption(ctx context.Context, msg *types.MsgAddAddressOption) (*types.MsgAddAddressOptionResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	// Bounded before the fee is taken, so a rejected option costs its submitter
	// only the gas rather than a burned ERTH.
	if err := types.ValidateDescription(msg.Description); err != nil {
		return nil, err
	}
	subBz, err := k.addressCodec.StringToBytes(msg.Submitter)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid submitter address")
	}
	gated := msg.Stream == types.STREAM_ID_GROUNDWORKS
	if gated && !bytes.Equal(subBz, k.GetAuthority()) {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner,
			"expected authority to add an address option to the groundworks stream")
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

	// The fee is the open path's spam brake, so it is charged only where that
	// path exists. Governance pays a proposal deposit instead, and burning from
	// the authority account would destroy protocol funds rather than a
	// submitter's.
	if !gated {
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
			if err := k.burnRecorder.RecordBurn(ctx, earthtypes.SourceAllocation, fee); err != nil {
				return nil, err
			}
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
