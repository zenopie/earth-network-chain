package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// Register verifies a proof-of-personhood proof, dedups on its nullifier, records
// the registration, mints 1 ANML, and pays the registration reward from the
// human stream's option #1 (50% registree / 50% referrer).
func (k msgServer) Register(ctx context.Context, msg *types.MsgRegister) (*types.MsgRegisterResponse, error) {
	creatorBz, err := k.addressCodec.StringToBytes(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	creator := sdk.AccAddress(creatorBz)

	nullifier, dsc, err := k.verifyRegistrationProof(ctx, creator, msg.Proof, msg.PublicSignals, msg.SignatureAlgorithm, msg.DscDer)
	if err != nil {
		return nil, err
	}

	// Bound how fast one Document Signer, or one country, can register people.
	// Checked only once the proof has verified and been bound to the
	// certificate: before that the signer named here is merely claimed, and
	// counting a claim would let anyone exhaust a legitimate signer's daily
	// allowance with junk that names it.
	if err := k.checkRegistrationRate(ctx, dsc.key, dsc.country); err != nil {
		return nil, err
	}

	// Settle the human stream up front: clearing a lapsed registration below
	// retires its vote weight, and that has to be credited against a current
	// index. Doing it once here also covers payRegistrationReward at the end.
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return nil, err
	}

	// Reject / clear an existing registration for this wallet.
	if reg, ok, err := k.getRegistrationByAddr(ctx, creator); err != nil {
		return nil, err
	} else if ok {
		expired, err := k.isExpired(ctx, reg)
		if err != nil {
			return nil, err
		}
		if !expired {
			return nil, errorsmod.Wrap(types.ErrAlreadyReg, "wallet already registered")
		}
		if err := k.removeRegistration(ctx, reg); err != nil {
			return nil, err
		}
	}

	// Move an existing registration for this person to this wallet.
	//
	// A live nullifier used to be refused outright, so losing the wallet you
	// registered from stranded your personhood until the registration lapsed --
	// a year by default -- with your passport in your hand and no way to present
	// it that the chain would accept.
	//
	// It is only safe to move it because the circuit binds the registrant's
	// address as a public input: a proof verifies only against the address it
	// was made for, so the proof that reached this line cannot have been lifted
	// out of somebody else's transaction. Without that binding this branch is
	// registration theft -- the proof bytes are public in every block.
	//
	// switched is what stops it also being a mint. A switch pays no registration
	// reward and mints no ANML: the person is already counted, and paying again
	// would let one human draw the reward pool down once per wallet.
	switched := false
	if reg, err := k.Registrations.Get(ctx, nullifier); err == nil {
		expired, err := k.isExpired(ctx, reg)
		if err != nil {
			return nil, err
		}
		// Only a live registration is a switch. An expired one is retired the
		// same way, but that person is re-entering rather than moving, so it
		// pays like any other registration.
		switched = !expired
		if err := k.removeRegistration(ctx, reg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}

	// Resolve the (optional) referrer: must be a distinct, currently-registered human.
	var referrer sdk.AccAddress
	if msg.Affiliate != "" {
		affBz, err := k.addressCodec.StringToBytes(msg.Affiliate)
		if err != nil {
			return nil, errorsmod.Wrap(types.ErrInvalidAffiliate, "invalid affiliate address")
		}
		if bytes.Equal(affBz, creatorBz) {
			return nil, errorsmod.Wrap(types.ErrInvalidAffiliate, "self-referral")
		}
		if _, err := k.requireValidHuman(ctx, sdk.AccAddress(affBz)); err != nil {
			return nil, errorsmod.Wrap(types.ErrInvalidAffiliate, "affiliate is not a registered human")
		}
		referrer = sdk.AccAddress(affBz)
	}

	// Record the registration.
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	reg := types.Registration{
		Nullifier:     nullifier,
		Address:       msg.Creator,
		RegisteredAt:  now,
		LastAnmlClaim: (now / 86400) * 86400,
		DscKey:        dsc.key,
		Country:       dsc.country,
	}
	if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
		return nil, err
	}
	if err := k.RegByAddr.Set(ctx, creatorBz, nullifier); err != nil {
		return nil, err
	}
	if err := k.RegByRegisteredAt.Set(ctx, collections.Join(now, nullifier)); err != nil {
		return nil, err
	}
	count, err := k.getRegCount(ctx)
	if err != nil {
		return nil, err
	}
	if err := k.RegCount.Set(ctx, count+1); err != nil {
		return nil, err
	}
	// Tally by Document Signer and issuing country. Recorded at registration
	// time because the certificate is only in hand here — nothing later can
	// recover which signer produced a given nullifier.
	if err := bumpCount(ctx, k.RegCountByDsc, dsc.key); err != nil {
		return nil, err
	}
	if err := bumpCount(ctx, k.RegCountByCountry, dsc.country); err != nil {
		return nil, err
	}
	// Index by signer so a revocation can find these again without scanning
	// every registration on the chain.
	if len(dsc.key) > 0 {
		if err := k.RegByDsc.Set(ctx, collections.Join(dsc.key, nullifier)); err != nil {
			return nil, err
		}
	}
	if err := k.recordRegistrationRate(ctx, dsc.key, dsc.country); err != nil {
		return nil, err
	}

	// Mint 1 ANML and pay the registration reward from the human stream's
	// option #1 -- neither on a switch, which moves a person already counted.
	reward := math.ZeroInt()
	if !switched {
		anml := sdk.NewCoins(sdk.NewInt64Coin(types.AnmlDenom, types.OneAnml))
		if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, anml); err != nil {
			return nil, err
		}
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, creator, anml); err != nil {
			return nil, err
		}

		reward, err = k.payRegistrationReward(ctx, creator, referrer)
		if err != nil {
			return nil, err
		}
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"register",
			sdk.NewAttribute("address", msg.Creator),
			sdk.NewAttribute("nullifier", hex.EncodeToString(nullifier)),
			sdk.NewAttribute("reward", reward.String()),
			sdk.NewAttribute("switched", strconv.FormatBool(switched)),
		),
	)

	return &types.MsgRegisterResponse{Reward: reward, Switched: switched}, nil
}
