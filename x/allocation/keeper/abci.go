package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	"github.com/earth-network/earth/x/allocation/types"
)

// BeginBlocker advances each stream's reward index, then settles and resolves
// only that stream's INTEGRATED options (each via its registered handler, e.g.
// compounding LP rewards into the dex pools). ADDRESS options are settled lazily
// on claim / vote change, so permissionless address options cost nothing per
// block.
//
// It then removes the options that have been dead long enough to go, capped so
// that a cohort falling due together cannot land unbounded work on one block.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	for _, stream := range types.Streams {
		if err := k.resolveIntegrated(ctx, stream); err != nil {
			return err
		}
	}
	// Emission first, housekeeping after: a dead option is removed only once it
	// has been dead for the whole grace period, so there is never anything owed
	// to it that this block's resolve would have paid.
	return k.SweepPrunableOptions(ctx)
}

func (k Keeper) resolveIntegrated(ctx context.Context, stream types.StreamId) error {
	// The only settle that retires leases: every one due by now, in time
	// order (lease.go).
	if err := k.SweepLapses(ctx, stream); err != nil {
		return err
	}
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return err
	}

	var ids []uint64
	rng := collections.NewPrefixedPairRange[uint32, uint64](key(stream))
	if err := k.IntegratedOptions.Walk(ctx, rng, func(k collections.Pair[uint32, uint64]) (bool, error) {
		ids = append(ids, k.K2())
		return false, nil
	}); err != nil {
		return err
	}

	for _, id := range ids {
		opt, err := k.Options.Get(ctx, optionKey(stream, id))
		if errors.Is(err, collections.ErrNotFound) {
			// A handler-set entry with no option (state from before pruneOption
			// cleared both): drop the entry rather than halt BeginBlock.
			if err := k.IntegratedOptions.Remove(ctx, optionKey(stream, id)); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		settleOption(&opt, rewardIndex)

		if h, ok := k.integratedHandlers[opt.Handler]; ok && h.stream == stream && opt.Accumulated.IsPositive() {
			// The handler is another module's code (the dex's LP rewards, ...)
			// run from BeginBlock: on its own branch, recovering panics. A
			// handler that fails resolves nothing this block; the balance stays
			// accrued on the option and is offered again next block.
			var resolved math.Int
			acc := opt.Accumulated
			if safeexec.Item(sdk.UnwrapSDKContext(ctx), types.ModuleName, "resolve_integrated", func(c sdk.Context) error {
				r, err := h.fn(c, acc)
				if err != nil {
					return err
				}
				if r.IsNil() || r.IsNegative() || r.GT(acc) {
					return fmt.Errorf("handler %s resolved %s of %s", opt.Handler, r, acc)
				}
				resolved = r
				return nil
			}) {
				opt.Accumulated = opt.Accumulated.Sub(resolved)
			}
		}

		// Through setOption like every other write, even though resolving an
		// integrated option moves only its accrued balance and never its weight.
		// One writer or none: an exception here is what a later edit that does
		// touch the weight would inherit.
		if err := k.setOption(ctx, stream, opt); err != nil {
			return err
		}
	}
	return nil
}
