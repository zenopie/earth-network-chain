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
)

// The caretaker stream's voters are anonymous. A split is filed in
// x/allocation under its caster's caretaker nullifier (one per identity
// secret, the same every time, so a refresh replaces it) at one fixed weight,
// and this module holds its lease: it counts until expires_at, R after it was
// cast, and the sweep then clears it. The chain cannot tell whose split it is,
// so it cannot clear it when a registration lapses or switches; the lease is
// the bound instead, and caretakerStatement's activation bound keeps a
// switched-to identity from voting beside a predecessor's live split.

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
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return 0, err
	}
	count, err := k.getCaretakerCount(ctx)
	if err != nil {
		return 0, err
	}
	for _, key := range lapsed {
		if err := k.allocationKeeper.ClearVoter(ctx, types.AllocationStream, key.K2()); err != nil {
			return 0, err
		}
		if err := k.CaretakerExpiry.Remove(ctx, key); err != nil {
			return 0, err
		}
		if err := k.CaretakerVotes.Remove(ctx, key.K2()); err != nil {
			return 0, err
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
