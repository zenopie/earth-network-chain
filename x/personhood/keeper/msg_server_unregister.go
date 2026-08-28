package keeper

import (
	"context"
	"encoding/hex"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// Unregister retires the signer's own registration and frees its nullifier.
//
// Until this existed the chain could only take a registration away — it lapses
// after registration_validity_seconds, or is purged when its Document Signer is
// revoked — and a person had no way to leave. The only remedy for a registration
// somebody wanted undone was resetting the chain from genesis, which is a thing
// that actually happened on 2026-08-26 for a single test registration.
//
// An expired registration can still be unregistered. It is retired either way,
// and refusing here would leave the row sitting in state until the expiry sweep
// reached it while telling the holder they were not registered.
func (k msgServer) Unregister(ctx context.Context, msg *types.MsgUnregister) (*types.MsgUnregisterResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}

	reg, ok, err := k.getRegistrationByAddr(ctx, sdk.AccAddress(creatorBz))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errorsmod.Wrap(types.ErrNotRegistered, msg.Creator)
	}

	// removeRegistration calls ClearVoter, which credits the retired vote weight
	// against the human stream's index -- so the index has to be current first.
	// Register does the same thing for the same reason.
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return nil, err
	}
	if err := k.removeRegistration(ctx, reg); err != nil {
		return nil, err
	}

	// The ANML already minted stays with the holder. It was minted for days they
	// were verified, and this is a departure rather than a reversal -- there is
	// no claim that the registration should never have existed.
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"unregister",
			sdk.NewAttribute("address", msg.Creator),
			sdk.NewAttribute("nullifier", hex.EncodeToString(reg.Nullifier)),
		),
	)

	return &types.MsgUnregisterResponse{Nullifier: reg.Nullifier}, nil
}
