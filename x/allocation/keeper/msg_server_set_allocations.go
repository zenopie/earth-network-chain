package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// SetAllocations sets the sender's split across one stream's options
// (percentages must sum to 100, or be empty to clear). Groundworks only: its
// weight is the sender's bonded stake. Caretaker splits are cast anonymously
// through x/personhood's MsgSetCaretaker, which proves a live registration
// without naming it.
func (k msgServer) SetAllocations(ctx context.Context, msg *types.MsgSetAllocations) (*types.MsgSetAllocationsResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	if msg.Stream == types.STREAM_ID_CARETAKER {
		return nil, errorsmod.Wrap(types.ErrUnknownStream,
			"caretaker splits are cast privately with x/personhood MsgSetCaretaker")
	}
	addrBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	weight, err := k.ApplySplit(ctx, msg.Stream, addrBz, msg.Percentages)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"set_allocations",
			sdk.NewAttribute("stream", msg.Stream.String()),
			sdk.NewAttribute("voter", msg.Creator),
			sdk.NewAttribute("weight", weight.String()),
		),
	)

	return &types.MsgSetAllocationsResponse{}, nil
}
