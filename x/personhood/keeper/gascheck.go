package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// CheckRegistration runs every check Register makes before its first write, and
// writes nothing. It answers "would this MsgRegister succeed, and for which
// person?" without a transaction.
//
// It exists for the gas-grant backend, via `earthd gas-check registration`. A
// new human has no ERTH and no account, so cannot pay for the registration that
// would earn them some; the backend pays for it, but only for a registration
// the chain would accept, and only once per passport. Running the chain's own
// checks rather than a copy of them is the point: a copy drifts at the next
// circuit or parameter change, and a drifted check either pays for
// registrations the chain refuses or refuses ones it would take.
//
// Not a consensus path. Nothing in the state machine calls this, and Register
// does not depend on it.
func (k Keeper) CheckRegistration(ctx context.Context, msg *types.MsgRegister) (nullifier []byte, switched bool, err error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, false, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	nullifier, dsc, err := k.verifyRegistrationProof(ctx, creator, msg.Proof, msg.PublicSignals, msg.SignatureAlgorithm, msg.DscDer)
	if err != nil {
		return nil, false, err
	}

	switched, err = k.isLiveRegistration(ctx, nullifier)
	if err != nil {
		return nil, false, err
	}
	if !switched {
		if err := k.checkRegistrationRate(ctx, dsc.key, dsc.country); err != nil {
			return nil, false, err
		}
	}

	if reg, ok, err := k.getRegistrationByAddr(ctx, creator); err != nil {
		return nil, false, err
	} else if ok {
		expired, err := k.isExpired(ctx, reg)
		if err != nil {
			return nil, false, err
		}
		if !expired {
			return nil, false, errorsmod.Wrap(types.ErrAlreadyReg, "wallet already registered")
		}
	}

	if msg.Affiliate != "" {
		affBz, err := k.addressCodec.StringToBytes(msg.Affiliate)
		if err != nil {
			return nil, false, errorsmod.Wrap(types.ErrInvalidAffiliate, "invalid affiliate address")
		}
		if bytes.Equal(affBz, creatorBz) {
			return nil, false, errorsmod.Wrap(types.ErrInvalidAffiliate, "self-referral")
		}
		if _, err := k.requireValidHuman(ctx, sdk.AccAddress(affBz)); err != nil {
			return nil, false, errorsmod.Wrap(types.ErrInvalidAffiliate, "affiliate is not a registered human")
		}
	}

	return nullifier, switched, nil
}

// CheckHuman returns addr's registration if it currently counts as a human:
// registered, not expired, and not under a revoked Document Signer. The same
// test every human-only message applies. For the gas-grant backend, like
// CheckRegistration.
func (k Keeper) CheckHuman(ctx context.Context, addr sdk.AccAddress) (types.Registration, error) {
	return k.requireValidHuman(ctx, addr)
}
