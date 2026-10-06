package keeper

import (
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

func handleKeeper(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		&countingBank{}, stubDex{}, nil, stubAllocation{}, &burnLog{}, stubShielded{})
	ctx := base.WithBlockTime(time.Unix(1_800_000_000, 0).UTC())
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{}) // as the private ante records them
	p := types.DefaultParams()
	p.HandleLeaseSeconds = 1000  // the lease
	p.HandleRenewalSeconds = 500 // the owner-only renewal period
	require.NoError(t, k.Params.Set(ctx, p))
	return k, ctx
}

func (k Keeper) statusOf(t *testing.T, ctx sdk.Context, h string) string {
	t.Helper()
	res, err := NewQueryServerImpl(k).Handle(ctx, &types.QueryHandleRequest{Handle: h})
	require.NoError(t, err)
	return res.Handle.Status
}

// Every handle transition: claim, uniqueness, one per human, the lease, the
// owner-only renewal period, release after it, a change freeing the old
// handle at once (claimable by another human in the same block), an
// explicit release, the sweep, and the genesis round trip.
func TestHandleLifecycle(t *testing.T) {
	k, ctx := handleKeeper(t)
	nfA := privacy.FieldBytes(privacy.U64(1))
	nfB := privacy.FieldBytes(privacy.U64(2))
	addrA, addrA2, addrB := personhoodtest.ShieldedAddress("A"), personhoodtest.ShieldedAddress("A2"), personhoodtest.ShieldedAddress("B")
	at := func(dt int64) sdk.Context { return ctx.WithBlockTime(time.Unix(ctx.BlockTime().Unix()+dt, 0)) }
	resolves := func(c sdk.Context, h string) bool {
		_, live, err := k.liveHandle(c, h)
		require.NoError(t, err)
		return live
	}

	// Claim; a second human is refused while it is live.
	exp, err := k.applyBindHandle(ctx, nfA, "alice", addrA)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Unix()+1000, exp)
	require.Equal(t, HandleLive, k.statusOf(t, ctx, "alice"))
	require.True(t, resolves(ctx, "alice"))
	_, err = k.applyBindHandle(ctx, nfB, "alice", addrB)
	require.ErrorIs(t, err, types.ErrHandleTaken)
	require.ErrorIs(t, k.handleClaimable(ctx, nfB, "alice"), types.ErrHandleTaken, "the ante's check")

	// Rebinding keeps the handle and may change the address.
	_, err = k.applyBindHandle(at(10), nfA, "alice", addrA2)
	require.NoError(t, err)
	rec, err := k.Handles.Get(ctx, "alice")
	require.NoError(t, err)
	got, err := rec.Address()
	require.NoError(t, err)
	require.Equal(t, addrA2, got)

	// The lease ends: it stops resolving, and for the renewal period only
	// its owner may renew it; an outsider is refused.
	lapsed := at(1010)
	require.Equal(t, HandleRenewal, k.statusOf(t, lapsed, "alice"))
	require.False(t, resolves(lapsed, "alice"))
	_, err = k.applyBindHandle(lapsed, nfB, "alice", addrB)
	require.ErrorIs(t, err, types.ErrHandleTaken, "an outsider in the renewal period")
	exp, err = k.applyBindHandle(lapsed, nfA, "alice", addrA)
	require.NoError(t, err, "the owner renews in the window")
	require.Equal(t, lapsed.BlockTime().Unix()+1000, exp)
	require.True(t, resolves(lapsed, "alice"))

	// Past the renewal period it is free: anyone may claim it.
	free := lapsed.WithBlockTime(time.Unix(exp+500, 0))
	require.Equal(t, HandleFree, k.statusOf(t, free, "alice"))
	_, err = k.applyBindHandle(free, nfB, "alice", addrB)
	require.NoError(t, err, "claimable after the window")
	_, ok := k.HandleByNf.Get(free, nfA)
	require.Error(t, ok, "A lost it")

	// One per human: A claiming another handle holds just that one.
	_, err = k.applyBindHandle(free, nfA, "amy", addrA)
	require.NoError(t, err)
	// A change frees the old handle at once: B claims it in the same block.
	_, err = k.applyBindHandle(free, nfA, "amy-2", addrA)
	require.NoError(t, err)
	require.Equal(t, HandleFree, k.statusOf(t, free, "amy"))
	cur, err := k.HandleByNf.Get(free, nfA)
	require.NoError(t, err)
	require.Equal(t, "amy-2", cur)
	nfC := privacy.FieldBytes(privacy.U64(3))
	_, err = k.applyBindHandle(free, nfC, "amy", personhoodtest.ShieldedAddress("C"))
	require.NoError(t, err, "the old handle is claimable in the same block")
	// Changing to a handle someone holds is refused, and A keeps its own.
	_, err = k.applyBindHandle(free, nfA, "amy", addrA)
	require.ErrorIs(t, err, types.ErrHandleTaken)
	require.Equal(t, HandleLive, k.statusOf(t, free, "amy-2"))

	// Explicit release: free at once.
	exp, err = k.applyBindHandle(free, nfA, "", privacy.ShieldedAddress{})
	require.NoError(t, err)
	require.Zero(t, exp)
	require.Equal(t, HandleFree, k.statusOf(t, free, "amy-2"))

	// Directory and genesis round trip.
	dir, err := NewQueryServerImpl(k).Handles(free, &types.QueryHandlesRequest{Limit: 1})
	require.NoError(t, err)
	require.Len(t, dir.Handles, 1)
	require.Equal(t, "alice", dir.Handles[0].Handle)
	require.Equal(t, "alice", dir.Next)
	dir, err = NewQueryServerImpl(k).Handles(free, &types.QueryHandlesRequest{Start: dir.Next})
	require.NoError(t, err)
	require.Len(t, dir.Handles, 1)
	require.Equal(t, "amy", dir.Handles[0].Handle)
	require.Empty(t, dir.Next)
	gs, err := k.ExportGenesis(free)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.Len(t, gs.Handles, 2)
	k2, ctx2 := handleKeeper(t)
	ctx2 = ctx2.WithBlockTime(free.BlockTime())
	require.NoError(t, k2.InitGenesis(ctx2, *gs))
	gs2, err := k2.ExportGenesis(ctx2)
	require.NoError(t, err)
	require.Equal(t, gs.Handles, gs2.Handles)
	require.True(t, func() bool { _, l, _ := k2.liveHandle(ctx2, "amy"); return l }())

	// The sweep deletes handles past their renewal period, not live ones.
	b, err := k.Handles.Get(free, "alice")
	require.NoError(t, err)
	gone := free.WithBlockTime(time.Unix(b.ExpiresAt+500, 0))
	n, err := k.sweepHandles(gone, 100)
	require.NoError(t, err)
	require.Equal(t, 2, n, "alice and amy (claimed in the same block, same lease)")
	_, err = k.Handles.Get(gone, "alice")
	require.Error(t, err)
}

// A claim by a nullifier holding no handle bounds the predecessor (now -
// the longest lease ever - margin); holders renew or change under any
// bound; a move hands the handle on and bars the mover from claiming again.
func TestHandlePredecessorAndMove(t *testing.T) {
	k, ctx := handleKeeper(t)
	require.NoError(t, k.noteHandleLease(ctx, types.DefaultParams())) // a longer lease once in force
	p, err := k.Params.Get(ctx)
	require.NoError(t, err)
	require.NoError(t, k.noteHandleLease(ctx, p)) // lowered back: the max stays
	bound, err := k.handleClaimBound(ctx)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Unix()-types.DefaultHandleLeaseSeconds-types.ActivationMarginSeconds, bound)

	nfA := privacy.FieldBytes(privacy.U64(1))
	nfA2 := privacy.FieldBytes(privacy.U64(2))
	addr := personhoodtest.ShieldedAddress("A")
	claim := func(nf []byte, h string, maxPred int64) error {
		_, err := k.handleStatement(ctx, &types.MsgBindHandle{Fee: feeStub(), Handle: h, Address: addr.Encode(),
			MaxPredecessor: uint64(maxPred), Membership: types.Membership{Nullifier: nf}})
		return err
	}
	require.NoError(t, claim(nfA, "alice", 0), "a fresh registrant claims at once")
	require.NoError(t, claim(nfA, "alice", bound-1))
	require.ErrorIs(t, claim(nfA, "alice", bound), types.ErrInvalidMsg, "a predecessor too recent")
	_, err = k.applyBindHandle(ctx, nfA, "alice", addr)
	require.NoError(t, err)
	require.NoError(t, claim(nfA, "alice", types.NoBound), "renewing: any bound")
	require.NoError(t, claim(nfA, "alice-2", types.NoBound), "changing: any bound")

	// Move to A2: lease kept; A may never claim again; A2 renews at once.
	before, err := k.Handles.Get(ctx, "alice")
	require.NoError(t, err)
	require.NoError(t, k.applyMoveHandle(ctx, nfA, "alice", nfA2))
	after, err := k.Handles.Get(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, nfA2, after.Nullifier)
	require.ErrorIs(t, claim(nfA, "other", 0), types.ErrHandleMovedOut)
	require.NoError(t, claim(nfA2, "alice", types.NoBound))
	require.ErrorIs(t, k.applyMoveHandle(ctx, nfA2, "alice", nfA), types.ErrHandleMovedOut, "nor receive one")
	nfB := privacy.FieldBytes(privacy.U64(3))
	_, err = k.applyBindHandle(ctx, nfB, "bob", personhoodtest.ShieldedAddress("B"))
	require.NoError(t, err)
	require.ErrorIs(t, k.applyMoveHandle(ctx, nfA2, "alice", nfB), types.ErrHandleTaken, "the new owner holds one")
}

// Audit 5 P2: one live handle per passport. The PoC: A claims "alice", the
// passport switches to B before A's renewal margin, B claims "bob" once its
// bound passes (alice is then in its renewal period), the passport switches
// back to A (predecessor_at = now), and A renewed alice unbounded: two live
// handles. A holder whose handle is not live now renews under the claim
// bound, and a handle that is not live cannot be moved.
func TestRenewalPeriodNeedsTheClaimBound(t *testing.T) {
	k, ctx := handleKeeper(t)
	nfA := privacy.FieldBytes(privacy.U64(1))
	nfB := privacy.FieldBytes(privacy.U64(2))
	addr := personhoodtest.ShieldedAddress("A")
	bind := func(c sdk.Context, nf []byte, h string, maxPred int64) error {
		_, err := k.handleStatement(c, &types.MsgBindHandle{Fee: feeStub(), Handle: h, Address: addr.Encode(),
			MaxPredecessor: uint64(maxPred), Membership: types.Membership{Nullifier: nf}})
		return err
	}
	at := func(dt int64) sdk.Context { return ctx.WithBlockTime(time.Unix(ctx.BlockTime().Unix()+dt, 0)) }

	// A claims alice; while it is live A renews under any bound.
	require.NoError(t, bind(ctx, nfA, "alice", 0))
	_, err := k.applyBindHandle(ctx, nfA, "alice", addr)
	require.NoError(t, err)
	require.NoError(t, bind(at(10), nfA, "alice", types.NoBound), "live: renew unbounded")

	// In the renewal period (lease 1000, window 500): A switched back, so its
	// leaf's predecessor_at is recent; renewing is a claim and is refused.
	lapsed := at(1010)
	require.Equal(t, HandleRenewal, k.statusOf(t, lapsed, "alice"))
	bound, err := k.handleClaimBound(lapsed)
	require.NoError(t, err)
	require.ErrorIs(t, bind(lapsed, nfA, "alice", lapsed.BlockTime().Unix()), types.ErrInvalidMsg,
		"a switched-back identity cannot revive its lapsed handle")
	require.ErrorIs(t, bind(lapsed, nfA, "alice-2", types.NoBound), types.ErrInvalidMsg,
		"nor change it into a new live one")
	// An identity whose predecessor is old enough (or none) renews as before.
	require.NoError(t, bind(lapsed, nfA, "alice", bound-1))
	require.NoError(t, bind(lapsed, nfA, "alice", 0))
	// Release needs no bound.
	require.NoError(t, bind(lapsed, nfA, "", types.NoBound))

	// A handle in its renewal period does not move.
	_, err = k.checkMoveHandle(lapsed, &types.MsgMoveHandle{Fee: feeStub(), Handle: "alice",
		Move: types.MoveProof{OldNullifier: nfA, NewNullifier: nfB}})
	require.ErrorIs(t, err, types.ErrInvalidMsg)
	require.Contains(t, err.Error(), "not live")
}

// The caretaker twin: a split past its expiry that the sweep has not reached
// is not held, so refreshing it is a new split under the bound.
func TestLapsedUnsweptSplitNeedsTheBound(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	now := ctx.BlockTime().Unix()
	nf := privacy.FieldBytes(privacy.U64(77))
	split := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	require.NoError(t, k.setCaretakerVote(ctx, nf, split, now+1000))
	m := &types.MsgSetCaretaker{Fee: feeStub(), MaxPredecessor: uint64(types.NoBound), Percentages: split,
		Membership: types.Membership{Nullifier: nf}}
	_, err := k.caretakerStatement(ctx, m)
	require.NoError(t, err, "live: refresh unbounded")
	lapsed := ctx.WithBlockTime(time.Unix(now+1000, 0))
	_, err = k.caretakerStatement(lapsed, m)
	require.ErrorIs(t, err, types.ErrInvalidMsg, "lapsed, unswept: bounded")
}

// Audit 5 P3: the lease lengths the bounds use are queryable. After
// governance cuts both leases, LeaseBounds keeps reporting the longer ones the
// chain still enforces, and its bounds are what the statements check.
func TestLeaseBoundsQuery(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	old, err := k.Params.Get(ctx)
	require.NoError(t, err)
	old.HandleLeaseSeconds = 5000
	old.CaretakerVoteSeconds = 4000
	require.NoError(t, k.noteHandleLease(ctx, old))
	require.NoError(t, k.Params.Set(ctx, old))
	next := old
	next.HandleLeaseSeconds = 1000
	next.CaretakerVoteSeconds = 1000
	require.NoError(t, k.holdLeaseSeconds(ctx, old, next))
	require.NoError(t, k.noteHandleLease(ctx, next))
	require.NoError(t, k.Params.Set(ctx, next))

	res, err := NewQueryServerImpl(k).LeaseBounds(ctx, &types.QueryLeaseBoundsRequest{})
	require.NoError(t, err)
	now := ctx.BlockTime().Unix()
	require.Equal(t, now, res.BlockTime)
	require.Equal(t, int64(5000), res.HandleLeaseSeconds)
	require.Equal(t, int64(4000), res.CaretakerLeaseSeconds)
	require.Equal(t, now+4000, res.CaretakerLeaseHoldUntil)
	hb, err := k.handleClaimBound(ctx)
	require.NoError(t, err)
	require.Equal(t, hb, res.HandleClaimBound)
	cb, err := k.LeaseActivationBound(ctx)
	require.NoError(t, err)
	require.Equal(t, cb, res.CaretakerCastBound)
	require.Equal(t, now-4000-types.ActivationMarginSeconds, res.CaretakerCastBound)
}
