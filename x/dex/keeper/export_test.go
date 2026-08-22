package keeper

import (
	"context"

	"github.com/earth-network/earth/x/dex/types"
)

// settleForTest exposes settlePoolRewards so the oracle tests can settle a pool
// without routing through a swap, which is the case the price accumulator used
// to miss.
func (k Keeper) SettleForTest(ctx context.Context, poolID uint64, pool *types.Pool) error {
	return k.settlePoolRewards(ctx, poolID, pool)
}
