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
// referrer and carries the referral note itself (MsgRegister
// affiliate_handle, affiliate_pc, affiliate_ciphertext), minted by the
// chain to the pc the registrant's wallet made for the handle's address.
//
// One handle per human: claimed with a membership proof in the handle scope
// (one nullifier per identity secret), under the caretaker activation rule
// (a switched-to identity cannot hold one beside its predecessor's).
//
// Lifecycle, for a record {handle, address, nullifier, expires_at}:
//
//   - live while now < expires_at: it resolves. Its owner renews it (a new
//     lease, now + caretaker_vote_seconds) by binding it again.
//   - renewal period while expires_at <= now < expires_at +
//     handle_renewal_seconds: it does not resolve (registrations naming it
//     are refused; wallets warn), and only the same nullifier may renew it.
//   - free after that: swept, anyone may claim it.
//
// A change to another handle frees the old one at once (no reservation),
// in the same msg that claims the new one; a release frees it at once.
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
	expiresAt := ctx.BlockTime().Unix() + params.CaretakerVoteSecondsOrDefault()
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

// handleStatement: scope handle, and the caretaker activation rule (see
// caretakerStatement): max_activation strictly before now - R - margin.
func (k Keeper) handleStatement(ctx context.Context, m *types.MsgBindHandle) (MembershipStatement, error) {
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	bound, err := k.LeaseActivationBound(ctx)
	if err != nil {
		return MembershipStatement{}, err
	}
	if bound <= 0 || m.MaxActivation >= uint64(bound) {
		return MembershipStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg,
			"max_activation %d is not before %d (now - lease length - activation margin)", m.MaxActivation, bound)
	}
	return MembershipStatement{Scope: privacy.HandleScope(), Signal: signal, MaxActivation: int64(m.MaxActivation)}, nil
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

// importHandles loads genesis handles.
func (k Keeper) importHandles(ctx context.Context, hs []types.Handle) error {
	for _, h := range hs {
		if err := k.putHandle(ctx, h); err != nil {
			return err
		}
	}
	return nil
}
