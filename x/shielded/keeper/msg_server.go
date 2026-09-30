package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

func (k msgServer) checkAuthority(authority string) error {
	a, err := k.addressCodec.StringToBytes(authority)
	if err != nil {
		return errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(k.GetAuthority(), a) {
		expected, _ := k.addressCodec.BytesToString(k.GetAuthority())
		return errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", expected, authority)
	}
	return nil
}

// UpdateParams replaces the module parameters.
func (k msgServer) UpdateParams(ctx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	if err := req.Params.Validate(); err != nil {
		return nil, err
	}
	if err := k.Params.Set(ctx, req.Params); err != nil {
		return nil, err
	}
	return &types.MsgUpdateParamsResponse{}, nil
}

// RegisterAsset admits a denom to the pool.
func (k msgServer) RegisterAsset(ctx context.Context, req *types.MsgRegisterAsset) (*types.MsgRegisterAssetResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	id, err := k.Keeper.RegisterAsset(ctx, req.Denom)
	if err != nil {
		return nil, err
	}
	return &types.MsgRegisterAssetResponse{AssetId: id}, nil
}

// Shield moves the sender's coins into the pool as one note.
func (k msgServer) Shield(ctx context.Context, msg *types.MsgShield) (*types.MsgShieldResponse, error) {
	sender, err := k.addressCodec.StringToBytes(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid sender address")
	}
	pos, cm, err := k.Keeper.Shield(ctx, sender, msg.Amount, msg.Pc, msg.Ciphertext)
	if err != nil {
		return nil, err
	}
	return &types.MsgShieldResponse{Position: pos, Commitment: cm}, nil
}

// Transfer completes a private transfer the ante has already verified and
// executed (inputs spent, outputs appended, fee paid): all that remains is
// paying value_out to the receiver.
//
// Refused unless the private ante authorized this transfer in this tx; see
// authorization.go for why that check is the whole of this msg's access
// control.
//
// The payout runs on an infinite gas meter. Its price was charged up front
// with the rest of the private msg's fixed gas, and an out-of-gas here would
// strand value the ante has already released from the notes.
func (k msgServer) Transfer(ctx context.Context, msg *types.MsgTransfer) (*types.MsgTransferResponse, error) {
	t := &msg.Transfer
	positions, err := AuthorizedPositions(ctx, t)
	if err != nil {
		return nil, err
	}
	if t.ValueOut > 0 {
		recv, err := msg.ReceiverBytes(k.addressCodec)
		if err != nil {
			return nil, err
		}
		unmetered := sdk.UnwrapSDKContext(ctx).WithGasMeter(storetypes.NewInfiniteGasMeter())
		if _, err := k.Unshield(unmetered, t, recv); err != nil {
			return nil, err
		}
	}
	return &types.MsgTransferResponse{Positions: positions}, nil
}
