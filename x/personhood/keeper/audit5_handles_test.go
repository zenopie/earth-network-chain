package keeper

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 5 P2: one live handle per passport. The PoC: A claims "alice", the
// passport switches to B before A's renewal margin, B claims "bob" once its
// bound passes (alice is then in its renewal period), the passport switches
// back to A (predecessor_at = now), and A renewed alice unbounded: two live
// handles. A holder whose handle is not live now renews under the claim
// bound, and a handle that is not live cannot be moved.
func TestAudit5RenewalPeriodNeedsTheClaimBound(t *testing.T) {
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
	_, err = k.checkMoveHandle(lapsed, &types.MsgMoveHandle{Fee: feeStub(), Handle: "alice", NewOwner: nfB,
		Membership: types.Membership{Nullifier: nfA}})
	require.ErrorIs(t, err, types.ErrInvalidMsg)
	require.Contains(t, err.Error(), "not live")
}

// The caretaker twin: a split past its expiry that the sweep has not reached
// is not held, so refreshing it is a new split under the bound.
func TestAudit5LapsedUnsweptSplitNeedsTheBound(t *testing.T) {
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
