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
// voters who come back and vote again. Governance-gated, and groundworks-only.
//
// The caretaker slate is not resettable. Stake-weighted x/gov is the capital
// axis; letting it retire the caretaker slate is the capital axis writing the
// persons axis's rules, and no matching lever points the other way. A reset
// confiscates nothing, but between the reset and the first new vote the stream
// accrues to nothing, so the power to fire it repeatedly is the power to
// silence the fund. That belongs to registered humans, who direct the slate by
// voting, and to nobody else — so the message rejects the stream outright
// rather than gating it on an authority.
//
// What this gives up is the sybil backstop: if counterfeit humans ever capture
// the caretaker slate, no on-chain lever clears it, and recovery is a binary
// upgrade. That is the trade — see readme.md, "The goal, and where it isn't
// reached yet".
//
// The other stream is untouched — the epochs are per stream precisely so that
// retiring a stale staker slate does not reach across to the human one.
// Stake and registrations are untouched either way, and options keep any ERTH
// they have already accrued: this redirects the future stream without
// confiscating anything already earned. Between the reset and the first new vote
// total weight is zero, so the stream accrues to nothing rather than to a stale
// slate.
func (k msgServer) ResetAllocations(ctx context.Context, msg *types.MsgResetAllocations) (*types.MsgResetAllocationsResponse, error) {
	if err := ValidateStream(msg.Stream); err != nil {
		return nil, err
	}
	if msg.Stream == types.STREAM_ID_CARETAKER {
		return nil, errorsmod.Wrap(types.ErrStreamNotResettable,
			"the caretaker slate is directed by registered humans and cannot be retired by governance")
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
