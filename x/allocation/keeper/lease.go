package keeper

import (
	"context"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	"github.com/earth-network/earth/x/allocation/types"
)

// Leased Groundworks splits. A split counts for groundworks_lease_seconds
// after it was cast or renewed (an operator's MsgSetAllocations here, a
// stake position's in x/shieldedstaking), as a caretaker split counts for
// caretaker_vote_seconds. The weight comes out at the exact lapse time:
// every settle of a stream (advanceIndexTo, whoever calls it: BeginBlock, a
// tx, a staking hook) first walks the leases lapsing up to its target in
// time order, settles the index to each lapse time and retires that
// weight there, then settles the rest of the way. So the emission after a
// lapse is never shared with the lapsed weight, however late anything
// looks.

// MaxLapsesPerSettle bounds the lapse times one settle walks. Splits are
// cast at most one per tx (and private ones per private action, at most
// max_private_actions_per_block a block), so a block's settle outpaces the
// rate leases can lapse at; leases left over (only after a long halt) lapse
// on the next settle, counting meanwhile.
const MaxLapsesPerSettle = 1000

// LapseRetrySeconds is how long a lease whose retirement failed waits
// before it is tried again (it counts meanwhile; the failure is evented).
const LapseRetrySeconds = 24 * 60 * 60

// GroundworksLeaseEnd is when a Groundworks split cast now lapses.
func (k Keeper) GroundworksLeaseEnd(ctx context.Context) (int64, error) {
	params, err := k.Params.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	return sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + int64(params.GroundworksLeaseSecondsOrDefault()), nil
}

// settleLapses retires, in time order, every lease of stream lapsing at or
// before now (unix nanos), settling the index to each lapse time first.
func (k Keeper) settleLapses(ctx context.Context, stream types.StreamId, now int64) error {
	lapsers := append([]types.Lapser{accountLapser{k: k, stream: stream}}, k.lapsers[stream]...)
	limit := now / int64(time.Second)
	for i := 0; i < MaxLapsesPerSettle; i++ {
		next, found := int64(0), false
		for _, l := range lapsers {
			t, ok, err := l.NextLapse(ctx, limit)
			if err != nil {
				return err
			}
			if ok && (!found || t < next) {
				next, found = t, true
			}
		}
		if !found {
			return nil
		}
		at := next * int64(time.Second)
		if last, err := k.getLastUpkeep(ctx, stream); err != nil {
			return err
		} else if last != 0 && at > last {
			if err := k.settleTo(ctx, stream, at); err != nil {
				return err
			}
		}
		for _, l := range lapsers {
			if t, ok, err := l.NextLapse(ctx, next); err != nil {
				return err
			} else if ok && t == next {
				if err := l.Lapse(ctx, next); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// accountLapser retires this module's leased account splits (operators'
// Groundworks splits).
type accountLapser struct {
	k      Keeper
	stream types.StreamId
}

func (a accountLapser) NextLapse(ctx context.Context, t int64) (int64, bool, error) {
	var out int64
	found := false
	rng := new(collections.Range[collections.Triple[int64, uint32, []byte]]).
		EndExclusive(collections.TriplePrefix[int64, uint32, []byte](t + 1))
	err := a.k.VoterLapses.Walk(ctx, rng, func(key collections.Triple[int64, uint32, []byte]) (bool, error) {
		if key.K2() != uint32(a.stream) {
			return false, nil
		}
		out, found = key.K1(), true
		return true, nil
	})
	return out, found, err
}

func (a accountLapser) Lapse(ctx context.Context, t int64) error {
	var due []collections.Triple[int64, uint32, []byte]
	rng := collections.NewSuperPrefixedTripleRange[int64, uint32, []byte](t, uint32(a.stream))
	if err := a.k.VoterLapses.Walk(ctx, rng, func(key collections.Triple[int64, uint32, []byte]) (bool, error) {
		due = append(due, key)
		return false, nil
	}); err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, key := range due {
		ok := safeexec.Item(sdkCtx, types.ModuleName, "lapse_split", func(c sdk.Context) error {
			if err := a.k.VoterLapses.Remove(c, key); err != nil {
				return err
			}
			v, err := a.k.Voters.Get(c, voterKey(a.stream, key.K3()))
			if errors.Is(err, collections.ErrNotFound) {
				return nil // cleared, or dropped by a reset, since
			} else if err != nil {
				return err
			}
			epoch, err := a.k.getEpoch(c, a.stream)
			if err != nil {
				return err
			}
			if v.ExpiresAt > t || v.Epoch != epoch {
				return nil // renewed (its new lease is queued), or stale
			}
			// The index is already at t: removing the contribution here is
			// the lapse.
			if err := a.k.writeVoter(c, a.stream, key.K3(), types.Voter{}, nil, false); err != nil {
				return err
			}
			c.EventManager().EmitEvent(sdk.NewEvent("split_lapsed",
				sdk.NewAttribute("stream", a.stream.String()),
				sdk.NewAttribute("voter", sdk.AccAddress(key.K3()).String()),
				sdk.NewAttribute("expires_at", strconv.FormatInt(t, 10)),
			))
			return nil
		})
		if !ok {
			// Counts for another day rather than stalling every settle on it.
			if err := a.k.VoterLapses.Remove(ctx, key); err != nil {
				return err
			}
			if err := a.k.VoterLapses.Set(ctx, collections.Join3(t+LapseRetrySeconds, key.K2(), key.K3())); err != nil {
				return err
			}
		}
	}
	return nil
}
