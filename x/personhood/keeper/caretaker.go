package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/types"

	"github.com/earth-network/earth/internal/safeexec"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"

	"github.com/earth-network/earth/zk/privacy"
)

// The caretaker stream's voters are anonymous. A split is filed in
// x/allocation under its caster's caretaker nullifier (one per identity
// secret, the same every time, so a refresh replaces it) at one fixed weight,
// and this module holds its lease: it counts until expires_at, R after it was
// cast, and the sweep then clears it. Nothing renews it on its own: its owner
// casts again (a refresh or a change). The chain cannot tell whose split it
// is, so it cannot clear it when a registration lapses or switches; the
// lease is the bound instead. caretakerStatement's predecessor bound keeps an
// identity that replaced another (switch or re-entry: the leaf's
// predecessor_at) from casting a new split beside its predecessor's live
// one; a fresh registrant casts at once; and a switch that wants to keep its
// split moves it to the successor identity's nullifier (MsgMoveCaretaker, a
// move proof: only to the same passport's next identity, so a daily switcher
// still holds one split).

// SetCaretaker casts, refreshes or (empty split) clears a caretaker split.
func (k msgServer) SetCaretaker(goCtx context.Context, msg *types.MsgSetCaretaker) (*types.MsgSetCaretakerResponse, error) {
	ctx, _, err := authorized[MembershipStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	nf := msg.Membership.Nullifier
	expiresAt := int64(0)
	if len(msg.Percentages) > 0 {
		expiresAt = ctx.BlockTime().Unix() + params.CaretakerVoteSecondsOrDefault()
	}
	if err := k.setCaretakerVote(ctx, nf, msg.Percentages, expiresAt); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("set_caretaker",
		sdk.NewAttribute("nullifier", hexOf(nf)),
		sdk.NewAttribute("expires_at", strconv.FormatInt(expiresAt, 10)),
	))
	return &types.MsgSetCaretakerResponse{ExpiresAt: expiresAt}, nil
}

func (k Keeper) getCaretakerCount(ctx context.Context) (uint64, error) {
	n, err := k.CaretakerCount.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return 0, nil
	}
	return n, err
}

// setCaretakerVote files (expiresAt > 0) or clears a split and its lease.
func (k Keeper) setCaretakerVote(ctx context.Context, nf []byte, percentages []allocationtypes.AllocationWeight, expiresAt int64) error {
	old, err := k.CaretakerVotes.Get(ctx, nf)
	existed := err == nil
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if existed {
		if err := k.CaretakerExpiry.Remove(ctx, collections.Join(old, nf)); err != nil {
			return err
		}
	}
	count, err := k.getCaretakerCount(ctx)
	if err != nil {
		return err
	}
	if expiresAt == 0 {
		if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
			return err
		}
		if err := k.allocationKeeper.ClearVoter(ctx, types.AllocationStream, nf); err != nil {
			return err
		}
		if !existed {
			return nil
		}
		if err := k.CaretakerVotes.Remove(ctx, nf); err != nil {
			return err
		}
		return k.CaretakerCount.Set(ctx, count-1)
	}
	if err := k.allocationKeeper.SetVoterSplit(ctx, types.AllocationStream, nf, percentages, math.NewInt(types.VoterWeight)); err != nil {
		return err
	}
	if err := k.CaretakerVotes.Set(ctx, nf, expiresAt); err != nil {
		return err
	}
	if err := k.CaretakerExpiry.Set(ctx, collections.Join(expiresAt, nf)); err != nil {
		return err
	}
	if !existed {
		return k.CaretakerCount.Set(ctx, count+1)
	}
	return nil
}

// sweepCaretakerVotes clears up to budget lapsed splits, returning how many.
func (k Keeper) sweepCaretakerVotes(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	var lapsed []collections.Pair[int64, []byte]
	if err := k.CaretakerExpiry.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if key.K1() > now {
			return true, nil
		}
		lapsed = append(lapsed, key)
		return len(lapsed) >= budget, nil
	}); err != nil {
		return 0, err
	}
	if len(lapsed) == 0 {
		return 0, nil
	}
	count, err := k.getCaretakerCount(ctx)
	if err != nil {
		return 0, err
	}
	for _, key := range lapsed {
		// Per entry, recovering panics (ClearVoter settles allocation maths).
		if !safeexec.Item(sdk.UnwrapSDKContext(ctx), types.ModuleName, "expire_caretaker", func(c sdk.Context) error {
			// Settle the stream up to the lease's own expiry, not to now:
			// the lapsed weight earns nothing after it, however late the
			// sweep (audit 4, C8). Lapsed keys are in expiry order, so the
			// index only moves forward.
			if err := k.allocationKeeper.AdvanceIndexTo(c, types.AllocationStream, key.K1()); err != nil {
				return err
			}
			if err := k.allocationKeeper.ClearVoter(c, types.AllocationStream, key.K2()); err != nil {
				return err
			}
			if err := k.CaretakerExpiry.Remove(c, key); err != nil {
				return err
			}
			return k.CaretakerVotes.Remove(c, key.K2())
		}) {
			continue
		}
		if count > 0 {
			count--
		}
	}
	return len(lapsed), k.CaretakerCount.Set(ctx, count)
}

// pruneClaimNullifiers drops claim nullifiers of days before yesterday: a
// claim is only ever for today, so nothing reads them.
func (k Keeper) pruneClaimNullifiers(ctx context.Context, limit int) error {
	today := uint64(sdk.UnwrapSDKContext(ctx).BlockTime().Unix()) / types.SecondsPerDay
	if today < 2 {
		return nil
	}
	var doomed []collections.Pair[uint64, []byte]
	rng := new(collections.Range[collections.Pair[uint64, []byte]]).EndExclusive(collections.PairPrefix[uint64, []byte](today - 1))
	if err := k.ClaimNullifiers.Walk(ctx, rng, func(key collections.Pair[uint64, []byte]) (bool, error) {
		doomed = append(doomed, key)
		return len(doomed) >= limit, nil
	}); err != nil {
		return err
	}
	for _, key := range doomed {
		if err := k.ClaimNullifiers.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// leaseSeconds is the lease length the activation bound uses: R, or a longer
// R governance lowered from while leases cast under it may still run (see
// types.LeaseHold).
func (k Keeper) leaseSeconds(ctx context.Context, params types.Params) (int64, error) {
	r := params.CaretakerVoteSecondsOrDefault()
	h, err := k.LeaseHold.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return r, nil
	} else if err != nil {
		return 0, err
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() < h.Until && h.Seconds > r {
		return h.Seconds, nil
	}
	return r, nil
}

// LeaseActivationBound is the latest activated_at a caretaker split or a
// handle may prove now: now - lease length - activation margin.
// A switched-to identity is activated at the switch, and its predecessor's
// leaf proves for at most a root window (<= the margin) after it, so every
// lease the predecessor could cast has lapsed before the successor may cast
// one.
func (k Keeper) LeaseActivationBound(ctx context.Context) (int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	r, err := k.leaseSeconds(ctx, params)
	if err != nil {
		return 0, err
	}
	return sdk.UnwrapSDKContext(ctx).BlockTime().Unix() - r - types.ActivationMarginSeconds, nil
}

// holdLeaseSeconds records, before params change to next, that the lease
// length in force (old, or a hold already running) keeps bounding activation
// until every lease cast under it has lapsed.
func (k Keeper) holdLeaseSeconds(ctx context.Context, old, next types.Params) error {
	cur, err := k.leaseSeconds(ctx, old)
	if err != nil {
		return err
	}
	if next.CaretakerVoteSecondsOrDefault() >= cur {
		return nil
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	h := types.LeaseHold{Seconds: cur, Until: now + cur}
	if prev, err := k.LeaseHold.Get(ctx); err == nil && prev.Until > h.Until {
		h.Until = prev.Until
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	return k.LeaseHold.Set(ctx, h)
}

// --- MsgMoveCaretaker -----------------------------------------------------

type moveCaretakerAction struct{ k Keeper }

func (a moveCaretakerAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	// Two leases and expiry entries, and the allocation resync of both
	// voters, priced as six note writes.
	return a.k.MembershipActionGas(ctx, 6)
}

// checkMoveCaretaker: old_nullifier holds a live split, new_nullifier holds
// none and never moved one away, and the proof's root is a recent identity
// root. The move proof shows new_nullifier is the successor's (same
// passport, live): one split per passport across a switch.
func (k Keeper) checkMoveCaretaker(ctx context.Context, m *types.MsgMoveCaretaker) (MoveStatement, error) {
	exp, err := k.CaretakerVotes.Get(ctx, m.Move.OldNullifier)
	if errors.Is(err, collections.ErrNotFound) {
		return MoveStatement{}, types.ErrInvalidMsg.Wrap("old_nullifier holds no caretaker split")
	} else if err != nil {
		return MoveStatement{}, err
	}
	if exp <= sdk.UnwrapSDKContext(ctx).BlockTime().Unix() {
		return MoveStatement{}, types.ErrInvalidMsg.Wrap("old_nullifier's caretaker split has lapsed")
	}
	if err := k.checkNewCaretakerOwner(ctx, m.Move.NewNullifier); err != nil {
		return MoveStatement{}, err
	}
	if err := k.CheckMove(ctx, m.Move); err != nil {
		return MoveStatement{}, err
	}
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MoveStatement{}, err
	}
	return MoveStatement{Scope: privacy.CaretakerScope(), Signal: signal}, nil
}

func (k Keeper) checkNewCaretakerOwner(ctx context.Context, owner []byte) error {
	if has, err := k.CaretakerVotes.Has(ctx, owner); err != nil {
		return err
	} else if has {
		return types.ErrInvalidMsg.Wrap("the successor already holds a caretaker split")
	}
	if moved, err := k.CaretakerMovedOut.Has(ctx, owner); err != nil {
		return err
	} else if moved {
		return types.ErrCaretakerMovedOut
	}
	return nil
}

// applyMoveCaretaker hands nf's split (and its expiry) to owner; nf may
// never cast again. Returns the expiry.
func (k Keeper) applyMoveCaretaker(ctx context.Context, nf, owner []byte) (int64, error) {
	if err := k.checkNewCaretakerOwner(ctx, owner); err != nil {
		return 0, err
	}
	exp, err := k.CaretakerVotes.Get(ctx, nf)
	if err != nil {
		return 0, err
	}
	if err := k.allocationKeeper.MoveVoter(ctx, types.AllocationStream, nf, owner); err != nil {
		return 0, err
	}
	if err := k.CaretakerExpiry.Remove(ctx, collections.Join(exp, nf)); err != nil {
		return 0, err
	}
	if err := k.CaretakerVotes.Remove(ctx, nf); err != nil {
		return 0, err
	}
	if err := k.CaretakerVotes.Set(ctx, owner, exp); err != nil {
		return 0, err
	}
	if err := k.CaretakerExpiry.Set(ctx, collections.Join(exp, owner)); err != nil {
		return 0, err
	}
	return exp, k.CaretakerMovedOut.Set(ctx, nf)
}

func (a moveCaretakerAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkMoveCaretaker(ctx, msg.(*types.MsgMoveCaretaker))
}

func (a moveCaretakerAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMove(ctx, msg.(*types.MsgMoveCaretaker).Move, prepared.(MoveStatement))
}

// ReleasedDenoms: a move only pays a fee.
func (moveCaretakerAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }

// MoveCaretaker hands old_nullifier's split to its successor's
// new_nullifier.
func (k msgServer) MoveCaretaker(goCtx context.Context, msg *types.MsgMoveCaretaker) (*types.MsgMoveCaretakerResponse, error) {
	ctx, _, err := authorized[MoveStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	exp, err := k.applyMoveCaretaker(ctx, msg.Move.OldNullifier, msg.Move.NewNullifier)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("move_caretaker",
		sdk.NewAttribute("nullifier", hexOf(msg.Move.NewNullifier)),
		sdk.NewAttribute("previous_nullifier", hexOf(msg.Move.OldNullifier)),
		sdk.NewAttribute("expires_at", strconv.FormatInt(exp, 10)),
	))
	return &types.MsgMoveCaretakerResponse{ExpiresAt: exp}, nil
}
