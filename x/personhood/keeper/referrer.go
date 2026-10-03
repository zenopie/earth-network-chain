package keeper

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Referrals are public. A registered human binds an address, with its
// owner's signed consent (see checkReferrerConsent), with
// MsgBindReferrer (a membership proof in the referrer scope: one nullifier per
// identity secret, the same every time); a registration naming that address
// as its affiliate pays the referrer's half of its reward there, in
// transparent ERTH. The chain knows that some live registration vouches for
// the address, not which.
//
// A binding lasts R (caretaker_vote_seconds) and is refreshed by the wallet;
// rebinding the same nullifier moves it to the new address, an empty address
// clears it, and the sweep clears lapsed ones. Like a caretaker split, it
// cannot be cleared when its binder's registration lapses or switches (the
// chain cannot tell whose it is); the lease is the bound, and the activation
// bound keeps a switched-to identity from holding a second binding beside its
// predecessor's.
//
// Self-referral. A registrant cannot name an address it bound itself: binding
// takes a live registration activated R + a root window ago, and the
// registration naming it is a new one. The residual case is re-entry: a
// person whose registration lapsed still has a live binding (for up to R),
// and re-registering the same passport as new pays the referrer's half to
// their own address. The chain cannot see that the binding's nullifier and the
// re-entering passport are the same person. It is bounded by the per-passport
// re-entry itself (a lapsed registration re-enters at most once per
// registration_validity_seconds) and costs at most the referral half.

// BindReferrer binds, moves or (empty address) clears a referrer binding.
func (k msgServer) BindReferrer(goCtx context.Context, msg *types.MsgBindReferrer) (*types.MsgBindReferrerResponse, error) {
	ctx, _, err := authorized[MembershipStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	nf := msg.Membership.Nullifier
	if err := k.removeReferrerBinding(ctx, nf); err != nil {
		return nil, err
	}
	expiresAt := int64(0)
	if msg.Address != "" {
		addr, err := k.addressCodec.StringToBytes(msg.Address)
		if err != nil {
			return nil, err
		}
		params, err := k.Params.Get(ctx)
		if err != nil {
			return nil, err
		}
		// Rechecked here: the ante's check ran against the same state, but a
		// second binding of the address in the same block would have passed
		// it too.
		if err := k.checkReferrerAddress(ctx, nf, addr); err != nil {
			return nil, err
		}
		expiresAt = ctx.BlockTime().Unix() + params.CaretakerVoteSecondsOrDefault()
		if err := k.putReferrerBinding(ctx, types.ReferrerBinding{Nullifier: nf, Address: msg.Address, ExpiresAt: expiresAt}, addr); err != nil {
			return nil, err
		}
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("bind_referrer",
		sdk.NewAttribute("nullifier", hexOf(nf)),
		sdk.NewAttribute("address", msg.Address),
		sdk.NewAttribute("expires_at", strconv.FormatInt(expiresAt, 10)),
	))
	return &types.MsgBindReferrerResponse{ExpiresAt: expiresAt}, nil
}

// checkReferrerAddress refuses an address bank will not pay, or one another
// live nullifier holds.
func (k Keeper) checkReferrerAddress(ctx context.Context, nf, addr []byte) error {
	if k.bankKeeper.BlockedAddr(addr) {
		return errorsmod.Wrap(types.ErrInvalidMsg, "a module account cannot be a referrer")
	}
	holder, err := k.ReferrerByAddr.Get(ctx, addr)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if string(holder) == string(nf) {
		return nil
	}
	b, err := k.ReferrerBindings.Get(ctx, holder)
	if err != nil {
		return err
	}
	if b.ExpiresAt > sdk.UnwrapSDKContext(ctx).BlockTime().Unix() {
		return types.ErrReferrerBound
	}
	// Lapsed but not yet swept: the new binding takes the address.
	return k.removeReferrerBinding(ctx, holder)
}

func (k Keeper) putReferrerBinding(ctx context.Context, b types.ReferrerBinding, addr []byte) error {
	if err := k.ReferrerBindings.Set(ctx, b.Nullifier, b); err != nil {
		return err
	}
	if err := k.ReferrerByAddr.Set(ctx, addr, b.Nullifier); err != nil {
		return err
	}
	return k.ReferrerExpiry.Set(ctx, collections.Join(b.ExpiresAt, b.Nullifier))
}

// removeReferrerBinding drops nf's binding and its indexes, if any.
func (k Keeper) removeReferrerBinding(ctx context.Context, nf []byte) error {
	b, err := k.ReferrerBindings.Get(ctx, nf)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := k.ReferrerExpiry.Remove(ctx, collections.Join(b.ExpiresAt, nf)); err != nil {
		return err
	}
	if addr, err := k.addressCodec.StringToBytes(b.Address); err == nil {
		if holder, err := k.ReferrerByAddr.Get(ctx, addr); err == nil && string(holder) == string(nf) {
			if err := k.ReferrerByAddr.Remove(ctx, addr); err != nil {
				return err
			}
		}
	}
	return k.ReferrerBindings.Remove(ctx, nf)
}

// liveReferrer reports whether addr holds a live binding, and until when.
func (k Keeper) liveReferrer(ctx context.Context, addr []byte) (bool, int64, error) {
	nf, err := k.ReferrerByAddr.Get(ctx, addr)
	if errors.Is(err, collections.ErrNotFound) {
		return false, 0, nil
	} else if err != nil {
		return false, 0, err
	}
	b, err := k.ReferrerBindings.Get(ctx, nf)
	if err != nil {
		return false, 0, err
	}
	return b.ExpiresAt > sdk.UnwrapSDKContext(ctx).BlockTime().Unix(), b.ExpiresAt, nil
}

// sweepReferrerBindings clears up to budget lapsed bindings, returning how
// many.
func (k Keeper) sweepReferrerBindings(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	var lapsed [][]byte
	if err := k.ReferrerExpiry.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if key.K1() > now {
			return true, nil
		}
		lapsed = append(lapsed, key.K2())
		return len(lapsed) >= budget, nil
	}); err != nil {
		return 0, err
	}
	for _, nf := range lapsed {
		if err := k.removeReferrerBinding(ctx, nf); err != nil {
			return 0, err
		}
	}
	return len(lapsed), nil
}

// --- the private action ---------------------------------------------------

type referrerAction struct{ k Keeper }

func (a referrerAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	// The binding, its two indexes, and the old binding's removal; and the
	// consent signature.
	g, err := a.k.MembershipActionGas(ctx, 4)
	if err != nil {
		return 0, err
	}
	return g + ReferrerConsentGas, nil
}

// ReferrerConsentGas prices the consent's secp256k1 verification (x/auth's
// sig_verify_cost_secp256k1 default).
const ReferrerConsentGas = 1000

// checkReferrerConsent refuses a binding of addr its owner did not sign for:
// the pubkey must be addr's, and its signature must verify over
// ReferrerConsentBytes for this chain, this nullifier, the consent's expiry
// height and this address. So nobody can squat an address they do not
// control (and keep its owner from binding it for a lease length at a
// time). The consent must not have expired (height <= expiry) and its expiry
// may be at most ReferrerConsentMaxBlocks ahead (audit 4, C10).
func checkReferrerConsent(chainID string, height int64, m *types.MsgBindReferrer, addr []byte) error {
	if len(m.ReferrerPubKey) != types.ReferrerPubKeyBytes || len(m.ReferrerSignature) != types.ReferrerSignatureBytes {
		return types.ErrNoReferrerConsent.Wrap("consent missing")
	}
	if height < 0 || m.ConsentExpiryHeight < uint64(height) {
		return types.ErrNoReferrerConsent.Wrapf("consent expired at height %d (now %d)", m.ConsentExpiryHeight, height)
	}
	if m.ConsentExpiryHeight-uint64(height) > types.ReferrerConsentMaxBlocks {
		return types.ErrNoReferrerConsent.Wrapf("consent expiry %d is more than %d blocks ahead of %d",
			m.ConsentExpiryHeight, types.ReferrerConsentMaxBlocks, height)
	}
	pk := &secp256k1.PubKey{Key: m.ReferrerPubKey}
	if !bytes.Equal(pk.Address(), addr) {
		return types.ErrNoReferrerConsent.Wrap("referrer_pub_key is not the address's key")
	}
	if !pk.VerifySignature(types.ReferrerConsentBytes(chainID, m.Membership.Nullifier, m.ConsentExpiryHeight, addr), m.ReferrerSignature) {
		return types.ErrNoReferrerConsent.Wrap("consent signature does not verify")
	}
	return nil
}

// referrerStatement: scope referrer, and the caretaker activation rule (see
// caretakerStatement): max_activation at most now - R - root window.
func (k Keeper) referrerStatement(ctx context.Context, m *types.MsgBindReferrer) (MembershipStatement, error) {
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	bound, err := k.LeaseActivationBound(ctx)
	if err != nil {
		return MembershipStatement{}, err
	}
	// Strictly before the bound (audit 4, C7): at max_activation == bound a
	// successor activated at the bound could file a lease in the very block
	// its predecessor's last lease lapses, both counted until the sweep.
	if bound <= 0 || m.MaxActivation >= uint64(bound) {
		return MembershipStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg,
			"max_activation %d is not before %d (now - lease length - activation margin)", m.MaxActivation, bound)
	}
	return MembershipStatement{
		Scope:         privacy.ReferrerScope(),
		Signal:        signal,
		MaxActivation: int64(m.MaxActivation),
	}, nil
}

func (k Keeper) checkBindReferrer(ctx context.Context, m *types.MsgBindReferrer) (MembershipStatement, error) {
	if m.Address != "" {
		addr, err := k.addressCodec.StringToBytes(m.Address)
		if err != nil {
			return MembershipStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg, "address: %v", err)
		}
		// Read-only here: a lapsed holder is only removed in the handler.
		if k.bankKeeper.BlockedAddr(addr) {
			return MembershipStatement{}, errorsmod.Wrap(types.ErrInvalidMsg, "a module account cannot be a referrer")
		}
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		if err := checkReferrerConsent(sdkCtx.ChainID(), sdkCtx.BlockHeight(), m, addr); err != nil {
			return MembershipStatement{}, err
		}
		if holder, err := k.ReferrerByAddr.Get(ctx, addr); err == nil && string(holder) != string(m.Membership.Nullifier) {
			if live, _, err := k.liveReferrer(ctx, addr); err != nil {
				return MembershipStatement{}, err
			} else if live {
				return MembershipStatement{}, types.ErrReferrerBound
			}
		} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return MembershipStatement{}, err
		}
	}
	if err := k.CheckMembership(ctx, m.Membership); err != nil {
		return MembershipStatement{}, err
	}
	return k.referrerStatement(ctx, m)
}

func (a referrerAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkBindReferrer(ctx, msg.(*types.MsgBindReferrer))
}

func (a referrerAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMembership(ctx, msg.(*types.MsgBindReferrer).Membership, prepared.(MembershipStatement))
}

// ReleasedDenoms: a referrer binding only pays a fee.
func (referrerAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }
