package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Handles: a registered human's name in a public directory, resolving to a
// shielded address (owner_pk, ek_pub). A wallet pays a handle by looking it
// up and making a note to its address (a private transfer, or MsgShield):
// the chain is only the directory. A registration may name a handle as its
// referrer (MsgRegister affiliate_handle); the chain mints the referral note
// itself to the address the handle resolves to, with an opening it derives
// and publishes (privacy.ReferralOpening), so the registrant cannot redirect
// it.
//
// One handle per human: claimed with a membership proof in the handle scope
// (one nullifier per identity secret), under the caretaker activation rule
// (a switched-to identity cannot hold one beside its predecessor's).
//
// Lifecycle, for a record {handle, address, nullifier, expires_at}:
//
//   - live while now < expires_at: it resolves. Its owner renews it (a new
//     lease, now + handle_lease_seconds) by binding it again; nothing renews
//     it on its own.
//   - renewal period while expires_at <= now < expires_at +
//     handle_renewal_seconds: it does not resolve (registrations naming it
//     are refused; wallets warn), only the same nullifier may renew it, and
//     only under the claim bound (as a claim), and it cannot be moved.
//   - free after that: swept, anyone may claim it.
//
// A change to another handle frees the old one at once (no reservation),
// in the same msg that claims the new one; a release frees it at once. A
// move (MsgMoveHandle) hands it, lease and all, to another handle nullifier:
// how an identity switch keeps its handle.
//
// One live handle per passport, across identity switches: a claim by a
// nullifier holding none needs a leaf whose predecessor_at (the switch or
// re-entry that made it; 0 for a passport never registered) is before now -
// the longest lease ever in force - the activation margin, so anything the
// predecessor identity held has lapsed. A fresh registrant claims at once. A
// nullifier that moved its handle away may never claim again (its identity
// handed its one handle on).
//
// Nobody's consent is needed to bind an address: naming someone else's
// shielded address only sends the binder's referrals to them.
//
// Self-referral. A registrant cannot name a handle it holds itself: claiming
// one takes a live registration activated R + a root window ago, and the
// registration naming it is a new one. The residual case is re-entry: a
// person whose registration lapsed may still hold a live handle (for up to
// R), and re-registering the same passport as new pays the referrer's half
// to their own handle. The chain cannot see that the handle's nullifier and
// the re-entering passport are the same person. It is bounded by the
// per-passport re-entry itself (a lapsed registration re-enters at most once
// per registration_validity_seconds) and costs at most the referral half.

// HandleStatus values (Query/Handle, Query/Handles).
const (
	HandleLive    = "live"
	HandleRenewal = "renewal"
	HandleFree    = "free"
)

// handleStatus is rec's status now, and its renewal deadline.
func (k Keeper) handleStatus(ctx context.Context, rec types.Handle) (string, int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return "", 0, err
	}
	until := rec.ExpiresAt + params.HandleRenewalSecondsOrDefault()
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	switch {
	case now < rec.ExpiresAt:
		return HandleLive, until, nil
	case now < until:
		return HandleRenewal, until, nil
	}
	return HandleFree, until, nil
}

// handleClaimable refuses handle if another nullifier holds it (live or in
// its renewal period).
func (k Keeper) handleClaimable(ctx context.Context, nf []byte, handle string) error {
	rec, err := k.Handles.Get(ctx, handle)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if string(rec.Nullifier) == string(nf) {
		return nil
	}
	st, until, err := k.handleStatus(ctx, rec)
	if err != nil {
		return err
	}
	if st != HandleFree {
		return types.ErrHandleTaken.Wrapf("%q is %s (reserved until %d)", handle, st, until)
	}
	return nil
}

func (k Keeper) putHandle(ctx context.Context, rec types.Handle) error {
	if err := k.Handles.Set(ctx, rec.Handle, rec); err != nil {
		return err
	}
	if err := k.HandleByNf.Set(ctx, rec.Nullifier, rec.Handle); err != nil {
		return err
	}
	return k.HandleRelease.Set(ctx, collections.Join(rec.ExpiresAt, rec.Handle))
}

// deleteHandle frees a handle at once: its record and indexes go.
func (k Keeper) deleteHandle(ctx context.Context, handle string) error {
	rec, err := k.Handles.Get(ctx, handle)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := k.HandleRelease.Remove(ctx, collections.Join(rec.ExpiresAt, handle)); err != nil {
		return err
	}
	if cur, err := k.HandleByNf.Get(ctx, rec.Nullifier); err == nil && cur == handle {
		if err := k.HandleByNf.Remove(ctx, rec.Nullifier); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.Handles.Remove(ctx, handle); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeHandleReleased,
		sdk.NewAttribute(types.AttributeKeyHandle, handle)))
	return nil
}

// applyBindHandle claims, renews or changes nf's handle (handle != "") with
// address addr, or releases it (handle == ""). It returns the new lease's
// end (0 for a release).
func (k Keeper) applyBindHandle(ctx sdk.Context, nf []byte, handle string, addr privacy.ShieldedAddress) (int64, error) {
	cur, err := k.HandleByNf.Get(ctx, nf)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	if handle == "" {
		if cur == "" {
			return 0, nil
		}
		return 0, k.deleteHandle(ctx, cur)
	}
	// Rechecked here: the ante checked the same state, but a claim of the
	// same handle earlier in the block would have passed it too.
	if err := k.handleClaimable(ctx, nf, handle); err != nil {
		return 0, err
	}
	if cur != "" && cur != handle {
		// A change: the old handle is free at once.
		if err := k.deleteHandle(ctx, cur); err != nil {
			return 0, err
		}
	}
	// A free record of another nullifier (past its renewal period, not yet
	// swept) goes first.
	if old, err := k.Handles.Get(ctx, handle); err == nil {
		if string(old.Nullifier) != string(nf) {
			if err := k.deleteHandle(ctx, handle); err != nil {
				return 0, err
			}
		} else if err := k.HandleRelease.Remove(ctx, collections.Join(old.ExpiresAt, handle)); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	expiresAt := ctx.BlockTime().Unix() + params.HandleLeaseSecondsOrDefault()
	if err := k.putHandle(ctx, types.Handle{
		Handle: handle, OwnerPk: privacy.FieldBytes(addr.OwnerPK), EkPub: addr.EKPub[:],
		Nullifier: nf, ExpiresAt: expiresAt,
	}); err != nil {
		return 0, err
	}
	return expiresAt, nil
}

// liveHandle returns handle's record if it resolves now.
func (k Keeper) liveHandle(ctx context.Context, handle string) (types.Handle, bool, error) {
	rec, err := k.Handles.Get(ctx, handle)
	if errors.Is(err, collections.ErrNotFound) {
		return rec, false, nil
	} else if err != nil {
		return rec, false, err
	}
	return rec, rec.ExpiresAt > sdk.UnwrapSDKContext(ctx).BlockTime().Unix(), nil
}

// sweepHandles deletes up to budget handles past their renewal period. One
// of runSweeps' sweeps.
func (k Keeper) sweepHandles(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() - params.HandleRenewalSecondsOrDefault()
	var due []string
	if err := k.HandleRelease.Walk(ctx, nil, func(key collections.Pair[int64, string]) (bool, error) {
		if key.K1() > cutoff {
			return true, nil
		}
		due = append(due, key.K2())
		return len(due) >= budget, nil
	}); err != nil {
		return 0, err
	}
	for _, h := range due {
		if err := k.deleteHandle(ctx, h); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

func (k Keeper) handleEntry(ctx context.Context, rec types.Handle) (types.HandleEntry, error) {
	a, err := rec.Address()
	if err != nil {
		return types.HandleEntry{}, err
	}
	st, until, err := k.handleStatus(ctx, rec)
	if err != nil {
		return types.HandleEntry{}, err
	}
	return types.HandleEntry{Handle: rec.Handle, Address: a.Encode(), Status: st, ExpiresAt: rec.ExpiresAt, RenewalUntil: until}, nil
}

// Handle implements the query.
func (q queryServer) Handle(ctx context.Context, req *types.QueryHandleRequest) (*types.QueryHandleResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	rec, err := q.k.Handles.Get(ctx, req.Handle)
	if errors.Is(err, collections.ErrNotFound) {
		return &types.QueryHandleResponse{Handle: types.HandleEntry{Handle: req.Handle, Status: HandleFree}}, nil
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	e, err := q.k.handleEntry(ctx, rec)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryHandleResponse{Found: true, Handle: e}, nil
}

// Handles implements the query: the directory in handle order.
func (q queryServer) Handles(ctx context.Context, req *types.QueryHandlesRequest) (*types.QueryHandlesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = types.HandleQueryDefaultLimit
	}
	if limit > types.HandleQueryMaxLimit {
		limit = types.HandleQueryMaxLimit
	}
	var rng collections.Ranger[string]
	if req.Start != "" {
		rng = new(collections.Range[string]).StartExclusive(req.Start)
	}
	res := &types.QueryHandlesResponse{}
	more := false
	if err := q.k.Handles.Walk(ctx, rng, func(_ string, rec types.Handle) (bool, error) {
		if len(res.Handles) == limit {
			more = true
			return true, nil
		}
		e, err := q.k.handleEntry(ctx, rec)
		if err != nil {
			return true, err
		}
		res.Handles = append(res.Handles, e)
		return false, nil
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if more {
		res.Next = res.Handles[len(res.Handles)-1].Handle
	}
	return res, nil
}

// --- the private action ---------------------------------------------------

type handleAction struct{ k Keeper }

func (a handleAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	// The record, its two indexes, and a released handle's removal.
	return a.k.MembershipActionGas(ctx, 4)
}

// handleClaimBound is the latest predecessor_at a leaf may carry to claim a
// handle while holding none: now - the longest handle lease ever in force -
// the activation margin.
func (k Keeper) handleClaimBound(ctx context.Context) (int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	lease := params.HandleLeaseSecondsOrDefault()
	if m, err := k.HandleLeaseMax.Get(ctx); err == nil && m > lease {
		lease = m
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	return sdk.UnwrapSDKContext(ctx).BlockTime().Unix() - lease - types.ActivationMarginSeconds, nil
}

// noteHandleLease records params' handle lease if it is the longest yet.
func (k Keeper) noteHandleLease(ctx context.Context, params types.Params) error {
	l := params.HandleLeaseSecondsOrDefault()
	if m, err := k.HandleLeaseMax.Get(ctx); err == nil && m >= l {
		return nil
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	return k.HandleLeaseMax.Set(ctx, l)
}

// handleStatement: scope handle, any activation; the predecessor bound (see
// handleClaimBound) for every bind that makes a handle live: a claim by a
// nullifier holding none, and a renewal or change by one whose handle is not
// live (its renewal period, or free but unswept). Only a nullifier holding a
// live handle renews or changes it unbounded: that handle was live all along,
// so the bind adds none. Audit 5 P2: a held handle in its renewal period was
// renewed unbounded, so an identity that switched away (B claims a second
// handle once its own bound passed) and back revived its first one: two live
// handles per passport.
func (k Keeper) handleStatement(ctx context.Context, m *types.MsgBindHandle) (MembershipStatement, error) {
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	if m.Handle != "" {
		nf := m.Membership.Nullifier
		holdsLive, err := k.holdsLiveHandle(ctx, nf)
		if err != nil {
			return MembershipStatement{}, err
		}
		if !holdsLive {
			if moved, err := k.HandleMovedOut.Has(ctx, nf); err != nil {
				return MembershipStatement{}, err
			} else if moved {
				return MembershipStatement{}, types.ErrHandleMovedOut
			}
			bound, err := k.handleClaimBound(ctx)
			if err != nil {
				return MembershipStatement{}, err
			}
			if err := checkPredecessorBound(m.MaxPredecessor, bound); err != nil {
				return MembershipStatement{}, err
			}
		}
	}
	return MembershipStatement{Scope: privacy.HandleScope(), Signal: signal,
		MaxActivation: types.NoBound, MaxPredecessor: int64(m.MaxPredecessor)}, nil
}

// holdsLiveHandle reports whether nf holds a handle that resolves now.
func (k Keeper) holdsLiveHandle(ctx context.Context, nf []byte) (bool, error) {
	cur, err := k.HandleByNf.Get(ctx, nf)
	if errors.Is(err, collections.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	_, live, err := k.liveHandle(ctx, cur)
	return live, err
}

func (k Keeper) checkBindHandle(ctx context.Context, m *types.MsgBindHandle) (MembershipStatement, error) {
	if _, _, err := m.ShieldedAddress(); err != nil {
		return MembershipStatement{}, err
	}
	if m.Handle != "" {
		if err := k.handleClaimable(ctx, m.Membership.Nullifier, m.Handle); err != nil {
			return MembershipStatement{}, err
		}
	}
	if err := k.CheckMembership(ctx, m.Membership); err != nil {
		return MembershipStatement{}, err
	}
	return k.handleStatement(ctx, m)
}

func (a handleAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkBindHandle(ctx, msg.(*types.MsgBindHandle))
}

func (a handleAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMembership(ctx, msg.(*types.MsgBindHandle).Membership, prepared.(MembershipStatement))
}

// ReleasedDenoms: a handle bind only pays a fee.
func (handleAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }

// BindHandle claims, renews, changes or releases the prover's handle.
func (k msgServer) BindHandle(goCtx context.Context, msg *types.MsgBindHandle) (*types.MsgBindHandleResponse, error) {
	ctx, _, err := authorized[MembershipStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	addr, _, err := msg.ShieldedAddress()
	if err != nil {
		return nil, err
	}
	nf := msg.Membership.Nullifier
	expiresAt, err := k.applyBindHandle(ctx, nf, msg.Handle, addr)
	if err != nil {
		return nil, err
	}
	if msg.Handle != "" {
		ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeHandleBound,
			sdk.NewAttribute(types.AttributeKeyHandle, msg.Handle),
			sdk.NewAttribute(types.AttributeKeyAddress, msg.Address),
			sdk.NewAttribute(types.AttributeKeyNullifier, hexOf(nf)),
			sdk.NewAttribute(types.AttributeKeyExpiresAt, strconv.FormatInt(expiresAt, 10)),
		))
	}
	return &types.MsgBindHandleResponse{ExpiresAt: expiresAt}, nil
}

// --- MsgMoveHandle ---------------------------------------------------------

type moveHandleAction struct{ k Keeper }

func (a moveHandleAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.MembershipActionGas(ctx, 4)
}

// checkMoveHandle: the prover holds handle and it is live, and new_owner
// holds none and never moved one away. Any activation and predecessor: a move
// creates no live handle. A handle in its renewal period does not move (audit
// 5 P2): the new owner could renew it unbounded, reviving a handle a switched
// identity had let lapse.
func (k Keeper) checkMoveHandle(ctx context.Context, m *types.MsgMoveHandle) (MembershipStatement, error) {
	cur, err := k.HandleByNf.Get(ctx, m.Membership.Nullifier)
	if errors.Is(err, collections.ErrNotFound) || (err == nil && cur != m.Handle) {
		return MembershipStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg, "the prover does not hold %q", m.Handle)
	} else if err != nil {
		return MembershipStatement{}, err
	}
	rec, err := k.Handles.Get(ctx, m.Handle)
	if err != nil {
		return MembershipStatement{}, err
	}
	if st, _, err := k.handleStatus(ctx, rec); err != nil {
		return MembershipStatement{}, err
	} else if st != HandleLive {
		return MembershipStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg, "%q is not live (%s): renew it before moving it", m.Handle, st)
	}
	if err := k.checkNewHandleOwner(ctx, m.NewOwner); err != nil {
		return MembershipStatement{}, err
	}
	if err := k.CheckMembership(ctx, m.Membership); err != nil {
		return MembershipStatement{}, err
	}
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	return MembershipStatement{Scope: privacy.HandleScope(), Signal: signal,
		MaxActivation: types.NoBound, MaxPredecessor: types.NoBound}, nil
}

func (k Keeper) checkNewHandleOwner(ctx context.Context, owner []byte) error {
	if has, err := k.HandleByNf.Has(ctx, owner); err != nil {
		return err
	} else if has {
		return errorsmod.Wrap(types.ErrHandleTaken, "new_owner already holds a handle")
	}
	if moved, err := k.HandleMovedOut.Has(ctx, owner); err != nil {
		return err
	} else if moved {
		return types.ErrHandleMovedOut
	}
	return nil
}

// applyMoveHandle hands handle (held by nf) to owner; nf may never claim
// again.
func (k Keeper) applyMoveHandle(ctx context.Context, nf []byte, handle string, owner []byte) error {
	if err := k.checkNewHandleOwner(ctx, owner); err != nil {
		return err
	}
	rec, err := k.Handles.Get(ctx, handle)
	if err != nil {
		return err
	}
	if string(rec.Nullifier) != string(nf) {
		return errorsmod.Wrapf(types.ErrInvalidMsg, "the prover does not hold %q", handle)
	}
	if err := k.HandleByNf.Remove(ctx, nf); err != nil {
		return err
	}
	rec.Nullifier = owner
	if err := k.putHandle(ctx, rec); err != nil {
		return err
	}
	return k.HandleMovedOut.Set(ctx, nf)
}

func (a moveHandleAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkMoveHandle(ctx, msg.(*types.MsgMoveHandle))
}

func (a moveHandleAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMembership(ctx, msg.(*types.MsgMoveHandle).Membership, prepared.(MembershipStatement))
}

// ReleasedDenoms: a move only pays a fee.
func (moveHandleAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }

// MoveHandle hands the prover's handle to new_owner.
func (k msgServer) MoveHandle(goCtx context.Context, msg *types.MsgMoveHandle) (*types.MsgMoveHandleResponse, error) {
	ctx, _, err := authorized[MembershipStatement](goCtx, msg)
	if err != nil {
		return nil, err
	}
	if err := k.applyMoveHandle(ctx, msg.Membership.Nullifier, msg.Handle, msg.NewOwner); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeHandleMoved,
		sdk.NewAttribute(types.AttributeKeyHandle, msg.Handle),
		sdk.NewAttribute(types.AttributeKeyNullifier, hexOf(msg.NewOwner)),
	))
	return &types.MsgMoveHandleResponse{}, nil
}

// importHandles loads genesis handles.
func (k Keeper) importHandles(ctx context.Context, hs []types.Handle) error {
	for _, h := range hs {
		if err := k.putHandle(ctx, h); err != nil {
			return err
		}
	}
	return nil
}
