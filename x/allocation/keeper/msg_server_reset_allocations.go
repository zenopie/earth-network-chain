package keeper

import (
	"bytes"
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// ResetAllocations clears every vote in one stream so it is only directed by
// voters who come back and vote again. Governance-gated.
//
// The other stream is untouched — the epochs are per stream precisely so that
// retiring a stale human slate does not also wipe out every staker's vote.
// Stake and registrations are untouched either way, and options keep any ERTH
// they have already accrued: this redirects the future stream without
// confiscating anything already earned. Between the reset and the first new vote
// total weight is zero, so the stream accrues to nothing rather than to a stale
// slate.
func (k msgServer) ResetAllocations(ctx context.Context, msg *types.MsgResetAllocations) (*types.MsgResetAllocationsResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	authBz, err := k.addressCodec.StringToBytes(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(authBz, k.GetAuthority()) {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "expected authority to reset allocations")
	}

	epoch, err := k.resetAllocations(ctx, msg.Stream)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"reset_allocations",
			sdk.NewAttribute("stream", msg.Stream.String()),
			sdk.NewAttribute("epoch", strconv.FormatUint(epoch, 10)),
		),
	)
	return &types.MsgResetAllocationsResponse{Epoch: epoch}, nil
}
