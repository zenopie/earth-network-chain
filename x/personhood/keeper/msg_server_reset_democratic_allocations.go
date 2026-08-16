package keeper

import (
	"bytes"
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// ResetDemocraticAllocations clears every democratic vote so the stream is only
// directed by humans who come back and vote again. Governance-gated.
//
// Registrations are untouched — nobody re-proves personhood, they only
// re-express a preference. Options and their accrued balances survive as well,
// so this redirects the future stream without confiscating anything already
// earned. Between the reset and the first new vote total weight is zero, so the
// stream accrues to nothing rather than to a stale slate.
func (k msgServer) ResetDemocraticAllocations(ctx context.Context, msg *types.MsgResetDemocraticAllocations) (*types.MsgResetDemocraticAllocationsResponse, error) {
	authBz, err := k.addressCodec.StringToBytes(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(authBz, k.GetAuthority()) {
		return nil, errorsmod.Wrap(types.ErrInvalidSigner, "expected authority to reset democratic allocations")
	}

	epoch, err := k.resetDemAllocations(ctx)
	if err != nil {
		return nil, err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"reset_democratic_allocations",
			sdk.NewAttribute("epoch", strconv.FormatUint(epoch, 10)),
		),
	)
	return &types.MsgResetDemocraticAllocationsResponse{Epoch: epoch}, nil
}
