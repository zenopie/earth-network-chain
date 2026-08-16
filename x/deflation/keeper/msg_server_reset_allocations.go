package keeper

import (
	"bytes"
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/deflation/types"
)

// ResetAllocations clears every stake-weighted allocation vote so the stream is
// only directed by stakers who come back and vote again. Governance-gated.
//
// Staked balances are untouched, and options keep any ERTH they have already
// accrued — this redirects the future stream without confiscating anything
// already earned. Between the reset and the first new vote total weight is zero,
// so the stream accrues to nothing rather than to a stale slate.
func (k msgServer) ResetAllocations(ctx context.Context, msg *types.MsgResetAllocations) (*types.MsgResetAllocationsResponse, error) {
	authBz, err := k.addressCodec.StringToBytes(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(authBz, k.GetAuthority()) {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "expected authority to reset allocations")
	}

	epoch, err := k.resetAllocations(ctx)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"reset_allocations",
			sdk.NewAttribute("epoch", strconv.FormatUint(epoch, 10)),
		),
	)
	return &types.MsgResetAllocationsResponse{Epoch: epoch}, nil
}
