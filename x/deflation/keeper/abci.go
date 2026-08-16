package keeper

import "context"

// BeginBlocker advances the allocation reward index, then settles and resolves
// only the INTEGRATED options (each via its registered handler, e.g. compounding
// LP rewards into the dex pools). ADDRESS options are settled lazily on claim /
// vote change, so permissionless address options cost nothing per block.
func (k Keeper) BeginBlocker(ctx context.Context) error {
	if err := k.advanceAllocationIndex(ctx); err != nil {
		return err
	}
	rewardIndex, err := k.getRewardIndex(ctx)
	if err != nil {
		return err
	}

	var ids []uint64
	if err := k.IntegratedOptions.Walk(ctx, nil, func(id uint64) (bool, error) {
		ids = append(ids, id)
		return false, nil
	}); err != nil {
		return err
	}

	for _, id := range ids {
		opt, err := k.AllocationOptions.Get(ctx, id)
		if err != nil {
			return err
		}
		settleOption(&opt, rewardIndex)

		if handler, ok := k.integratedHandlers[opt.Handler]; ok && opt.Accumulated.IsPositive() {
			resolved, err := handler(ctx, opt.Accumulated)
			if err != nil {
				return err
			}
			opt.Accumulated = opt.Accumulated.Sub(resolved)
		}

		if err := k.AllocationOptions.Set(ctx, id, opt); err != nil {
			return err
		}
	}
	return nil
}
