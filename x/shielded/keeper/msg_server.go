package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"

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

// Send completes a private send the ante has already verified and executed:
// inputs spent, outputs appended, the fee paid and any unshield paid to the
// receiver, all atomically in the ante. What remains is reporting where the
// outputs landed.
//
// Refused unless the private ante authorized this very msg in this tx; see
// authorization.go for why that check is the whole of this msg's access
// control.
func (k msgServer) Send(ctx context.Context, msg *types.MsgSend) (*types.MsgSendResponse, error) {
	positions, err := AuthorizedPositions(ctx, msg)
	if err != nil {
		return nil, err
	}
	var out []uint64
	if len(positions) > 0 {
		out = positions[0]
	}
	return &types.MsgSendResponse{Positions: out}, nil
}
