package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// authorized returns what the private ante prepared for msg, refusing a msg
// that did not come through it (a contract's CosmosMsg::Any, an ICA host tx:
// a zero-signer msg passes their signer checks vacuously). The returned
// context runs on an infinite gas meter: the action's work was priced up front
// with its fixed gas, and running out halfway would strand a fee already paid.
func authorized[T any](ctx context.Context, msg shieldedtypes.PrivateMsg) (sdk.Context, T, error) {
	var zero T
	a, err := shieldedkeeper.AuthorizedAction(ctx, msg)
	if err != nil {
		return sdk.Context{}, zero, err
	}
	p, ok := a.(T)
	if !ok {
		return sdk.Context{}, zero, shieldedtypes.ErrUnauthorized.Wrap("private action prepared for another msg")
	}
	return sdk.UnwrapSDKContext(ctx).WithGasMeter(storetypes.NewInfiniteGasMeter()), p, nil
}

// Register applies a registration the private ante has checked, verified and
// collected the fee for.
//
// New, or re-entering after the last registration under this passport lapsed:
// the leaf is appended, and 1 ANML and the registrant's reward are minted as
// notes to the pcs the proof is bound to; the referrer's half is minted by
// the chain to the affiliate handle's address (referralNoteFor). A live
// registration under this passport makes it a switch:
// the old leaf is zeroed, the new one appended with a fresh activated_at, and
// nothing is paid, and only the signer's daily count moves (the person is
// already counted).
func (k msgServer) Register(goCtx context.Context, msg *types.MsgRegister) (*types.MsgRegisterResponse, error) {
	ctx, p, err := authorized[preparedRegistration](goCtx, msg)
	if err != nil {
		return nil, err
	}
	now := ctx.BlockTime().Unix()

	switched := false
	if old, err := k.Registrations.Get(ctx, p.nullifier); err == nil {
		expired, err := k.isExpired(ctx, old)
		if err != nil {
			return nil, err
		}
		switched = !expired
		if err := k.removeRegistration(ctx, old); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if switched != p.switched {
		// Nothing runs between the ante and here, so this cannot happen; if it
		// did, paying on a stale decision would be worse than refusing.
		return nil, types.ErrInvalidMsg.Wrap("registration state changed since the ante")
	}

	if err := k.markBindingUsed(ctx, p.binding, p.proofDate); err != nil {
		return nil, err
	}

	// predecessor_at: the switch or re-entry that made this leaf, or 0 for a
	// passport never registered before. A re-entry (the passport's earlier
	// registration lapsed or was purged) has a predecessor too: whatever it
	// held may still be live.
	predecessorAt := int64(0)
	if seen, err := k.PassportsSeen.Has(ctx, p.nullifier); err != nil {
		return nil, err
	} else if seen || switched {
		predecessorAt = now
	}
	if err := k.PassportsSeen.Set(ctx, p.nullifier); err != nil {
		return nil, err
	}
	leaf, err := IdentityLeaf(msg.Idc, p.dsc.key, p.dsc.country, now, predecessorAt)
	if err != nil {
		return nil, err
	}
	index, err := k.appendLeaf(ctx, leaf)
	if err != nil {
		return nil, err
	}
	if err := k.addRegistration(ctx, types.Registration{
		Nullifier:     p.nullifier,
		LeafIndex:     index,
		RegisteredAt:  now,
		ActivatedAt:   now,
		PredecessorAt: predecessorAt,
		DscKey:        p.dsc.key,
		Country:       p.dsc.country,
		Idc:           msg.Idc,
	}); err != nil {
		return nil, err
	}

	paid := registrationPayout{registrant: math.ZeroInt(), referral: math.ZeroInt()}
	referred := false
	if switched {
		if err := k.recordSwitchRate(ctx, p.dsc.key); err != nil {
			return nil, err
		}
	} else {
		if err := k.recordRegistrationRate(ctx, p.dsc.key, p.dsc.country); err != nil {
			return nil, err
		}
		if _, err := k.mintAnmlNote(ctx, msg.PcAnml, msg.CiphertextAnml); err != nil {
			return nil, err
		}
		var referrer *referralNote
		if p.referred {
			if referrer, err = k.referralNoteFor(ctx, msg.AffiliateHandle, p.nullifier, index); err != nil {
				return nil, err
			}
			referred = referrer != nil
		}
		paid, err = k.payRegistrationReward(ctx, rewardNote{pc: msg.PcErth, ciphertext: msg.CiphertextErth}, referrer)
		if err != nil {
			return nil, err
		}
	}

	attrs := []sdk.Attribute{
		sdk.NewAttribute("nullifier", hexOf(p.nullifier)),
		sdk.NewAttribute("leaf_index", strconv.FormatUint(index, 10)),
		sdk.NewAttribute("reward", paid.registrant.String()),
		sdk.NewAttribute("switched", strconv.FormatBool(switched)),
	}
	if referred {
		// The referral: the handle, what it was paid and where the note is
		// (its opening is on that position's shielded_mint event). Public: the
		// handle and the amount are what MsgRegister and the draw already show.
		attrs = append(attrs,
			sdk.NewAttribute(types.AttributeKeyHandle, msg.AffiliateHandle),
			sdk.NewAttribute("referral", paid.referral.String()))
		if paid.referral.IsPositive() {
			attrs = append(attrs, sdk.NewAttribute("referral_position", strconv.FormatUint(paid.referralPosition, 10)))
		}
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("register", attrs...))
	reward := paid.registrant
	return &types.MsgRegisterResponse{Reward: reward, Switched: switched, LeafIndex: index}, nil
}
