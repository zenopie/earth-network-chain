package keeper

import (
	"context"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/allocation/types"
)

// BeginBlocker advances each stream's reward index, then settles and resolves
// only that stream's INTEGRATED options (each via its registered handler, e.g.
// compounding LP rewards into the dex pools). ADDRESS options are settled lazily
// on claim / vote change, so permissionless address options cost nothing per
// block.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	for _, stream := range types.Streams {
		if err := k.resolveIntegrated(ctx, stream); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) resolveIntegrated(ctx context.Context, stream types.StreamId) error {
	if err := k.AdvanceIndex(ctx, stream); err != nil {
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
		if err != nil {
			return err
		}
		settleOption(&opt, rewardIndex)

		if h, ok := k.integratedHandlers[opt.Handler]; ok && h.stream == stream && opt.Accumulated.IsPositive() {
			resolved, err := h.fn(ctx, opt.Accumulated)
			if err != nil {
				return err
			}
			opt.Accumulated = opt.Accumulated.Sub(resolved)
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
