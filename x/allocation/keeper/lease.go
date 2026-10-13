package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	"github.com/earth-network/earth/x/allocation/types"
)

// Leased Groundworks splits. A split counts for groundworks_lease_seconds
// after it was cast or renewed (an operator's MsgSetAllocations), as a
// caretaker split counts for caretaker_vote_seconds. The weight comes out at
// the exact lapse time. Operators' splits are the only leased weight: the
// one lease queue is VoterLapses (x/shieldedstaking's stake note votes carry
// no lease since v1.3.0; they end with their stake vintage).
//
// Leases retire in one place only: this module's BeginBlocker
// (SweepLapses), which walks every lease due up to the block time, in time
// order, settles the index to each lapse time, retires that weight there,
// then settles the rest of the way. It drains the whole due queue, however
// long the chain was halted, so after it nothing is due for the rest of the
// block. Every other settle (a tx, a staking hook, an EndBlock resync) only
// moves the index and never touches a voter, position or total. A caller
// that reads one, settles, and writes it back cannot undo or double a lapse
// (audit round 2, CD-1: before, any settle could retire leases, and a
// backlog past one settle's cap left some due at tx time, where a
// read-settle-write resurrected lapsed weight, subtracted a position twice
// or re-filed a lapsed operator vote with no lease).
//
// Should a settle ever run while a lease is due (only if a module ordered
// before x/allocation settled Groundworks in BeginBlock; none does), it
// stops the index at that lapse time instead of passing it, so the emission
// after a lapse is still never shared with the lapsed weight, and emits
// EventTypeLeaseSettleHeld. The sweep goes on from there.

// LapseBacklogAlert is the number of lapse seconds one sweep walks above
// which it reports EventTypeLeaseBacklogDrained. Splits are cast at block
// times, so at most one new lapse second falls due per block in normal
// running; a sweep walking more than this is draining a halt's backlog.
const LapseBacklogAlert = 1000

// LapseRetrySeconds is how long a lease whose retirement failed waits
// before it is tried again (it counts meanwhile; the failure is evented as
// EventTypeLeaseRetireFailed).
const LapseRetrySeconds = 24 * 60 * 60

// GroundworksLeaseEnd is when a Groundworks split cast now lapses.
func (k Keeper) GroundworksLeaseEnd(ctx context.Context) (int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	return sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + int64(params.GroundworksLeaseSecondsOrDefault()), nil
}

// LapseRetryAt is when a lease due at t whose retirement failed now is
// tried again: a day after the later of t and the block time, so a retry is
// never due again within the sweep that failed it.
func LapseRetryAt(ctx context.Context, t int64) int64 {
	if now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix(); now > t {
		t = now
	}
	return t + LapseRetrySeconds
}

// EmitLeaseRetireFailed reports a lease that could not be retired at t and
// keeps counting until retryAt. lapser names the queue: always "account"
// (operators' splits) since plug-in lease queues were removed, kept as an
// attribute so indexers that read it do not break. key is the voter.
func EmitLeaseRetireFailed(ctx context.Context, stream types.StreamId, lapser, key string, t, retryAt int64, cause error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.Logger().Error("allocation: a lease failed to retire; it counts until the retry",
		"stream", stream.String(), "lapser", lapser, "key", key, "expires_at", t, "retry_at", retryAt, "err", cause)
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeLeaseRetireFailed,
		sdk.NewAttribute(types.AttributeKeyStream, stream.String()),
		sdk.NewAttribute(types.AttributeKeyLapser, lapser),
		sdk.NewAttribute(types.AttributeKeyKey, key),
		sdk.NewAttribute(types.AttributeKeyExpiresAt, strconv.FormatInt(t, 10)),
		sdk.NewAttribute(types.AttributeKeyRetryAt, strconv.FormatInt(retryAt, 10)),
		sdk.NewAttribute(types.AttributeKeyError, cause.Error()),
	))
}

// SweepLapses retires, in time order, every lease of stream due at or
// before the block time, settling the index to each lapse time first, then
// settles the stream to the block time. The BeginBlocker's settle; the only
// place a lease retires.
//
// Unbounded on purpose: leaving any lease due would let a later settle in
// the block meet it (CD-1). The work is one retirement per split cast a
// lease length before, so after a halt of H seconds it is the casts of an H
// long window, done in one block (and reported, EventTypeLeaseBacklogDrained).
func (k Keeper) SweepLapses(ctx context.Context, stream types.StreamId) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	limit := now / int64(time.Second)
	walked := 0
	prev, started := int64(0), false
	for {
		next, found, err := k.nextLapse(ctx, stream, limit)
		if err != nil {
			return err
		}
		if !found {
			break
		}
		// lapse leaves nothing at the time it retired (a lease it cannot
		// retire is re-filed later): one left behind would loop here for ever.
		if started && next <= prev {
			return fmt.Errorf("allocation: a lease due at %d was left behind (last retired %d)", next, prev)
		}
		prev, started = next, true
		walked++
		at := next * int64(time.Second)
		if last, err := k.getLastUpkeep(ctx, stream); err != nil {
			return err
		} else if last != 0 && at > last {
			if err := k.settleTo(ctx, stream, at); err != nil {
				return err
			}
		}
		if err := k.lapse(ctx, stream, next); err != nil {
			return err
		}
	}
	if walked > LapseBacklogAlert {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeLeaseBacklogDrained,
			sdk.NewAttribute(types.AttributeKeyStream, stream.String()),
			sdk.NewAttribute(types.AttributeKeyLapseSeconds, strconv.Itoa(walked)),
		))
	}
	return k.settleTo(ctx, stream, now)
}

// heldTarget is how far a settle that is not the sweep may move stream:
// now, or the earliest lease due before it (the sweep retires that one).
// Nothing is due once this block's sweep has run.
func (k Keeper) heldTarget(ctx context.Context, stream types.StreamId, now int64) (int64, error) {
	t, ok, err := k.nextLapse(ctx, stream, now/int64(time.Second))
	if err != nil || !ok {
		return now, err
	}
	if at := t * int64(time.Second); at < now {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeLeaseSettleHeld,
			sdk.NewAttribute(types.AttributeKeyStream, stream.String()),
			sdk.NewAttribute(types.AttributeKeyExpiresAt, strconv.FormatInt(t, 10)),
		))
		return at, nil
	}
	return now, nil
}

// nextLapse is the earliest lease of stream (operators' leased splits) due
// at or before t (unix seconds), if any.
func (k Keeper) nextLapse(ctx context.Context, stream types.StreamId, t int64) (int64, bool, error) {
	var out int64
	found := false
	rng := new(collections.Range[collections.Triple[int64, uint32, []byte]]).
		EndExclusive(collections.TriplePrefix[int64, uint32, []byte](t + 1))
	err := k.VoterLapses.Walk(ctx, rng, func(key collections.Triple[int64, uint32, []byte]) (bool, error) {
		if key.K2() != uint32(stream) {
			return false, nil
		}
		out, found = key.K1(), true
		return true, nil
	})
	return out, found, err
}

// lapse retires every lease of stream due at exactly t. The stream's index
// is already settled to t, and the voters are written without settling
// further, so the emission after a lapse is never shared with the lapsed
// weight. A lease it cannot retire is re-filed a day on, so no entry at t
// is left behind.
func (k Keeper) lapse(ctx context.Context, stream types.StreamId, t int64) error {
	var due []collections.Triple[int64, uint32, []byte]
	rng := collections.NewSuperPrefixedTripleRange[int64, uint32, []byte](t, uint32(stream))
	if err := k.VoterLapses.Walk(ctx, rng, func(key collections.Triple[int64, uint32, []byte]) (bool, error) {
		due = append(due, key)
		return false, nil
	}); err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, key := range due {
		itemErr := safeexec.Cached(sdkCtx, func(c sdk.Context) error {
			if err := k.VoterLapses.Remove(c, key); err != nil {
				return err
			}
			v, err := k.Voters.Get(c, voterKey(stream, key.K3()))
			if errors.Is(err, collections.ErrNotFound) {
				return nil // cleared, or dropped by a reset, since
			} else if err != nil {
				return err
			}
			epoch, err := k.getEpoch(c, stream)
			if err != nil {
				return err
			}
			if v.ExpiresAt > t || v.Epoch != epoch {
				return nil // renewed (its new lease is queued), or stale
			}
			// The index is already at t: removing the contribution here is
			// the lapse.
			if err := k.writeVoter(c, stream, key.K3(), types.Voter{}, nil, false); err != nil {
				return err
			}
			c.EventManager().EmitEvent(sdk.NewEvent("split_lapsed",
				sdk.NewAttribute("stream", stream.String()),
				sdk.NewAttribute("voter", sdk.AccAddress(key.K3()).String()),
				sdk.NewAttribute("expires_at", strconv.FormatInt(t, 10)),
			))
			return nil
		})
		if itemErr != nil {
			// Counts for another day rather than stalling every sweep on it.
			retry := LapseRetryAt(ctx, t)
			if err := k.VoterLapses.Remove(ctx, key); err != nil {
				return err
			}
			if err := k.VoterLapses.Set(ctx, collections.Join3(retry, key.K2(), key.K3())); err != nil {
				return err
			}
			EmitLeaseRetireFailed(ctx, stream, "account", sdk.AccAddress(key.K3()).String(), t, retry, itemErr)
		}
	}
	return nil
}
