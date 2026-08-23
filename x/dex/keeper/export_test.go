package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// settleForTest exposes settlePoolRewards so the oracle tests can settle a pool
// without routing through a swap, which is the case the price accumulator used
// to miss.
func (k Keeper) SettleForTest(ctx context.Context, poolID uint64, pool *types.Pool) error {
	return k.settlePoolRewards(ctx, poolID, pool)
}

// ApplyVolumeForTest records swap volume against a pool without routing a whole
// swap through the AMM, so a test can pin the scaling index's effect on a known
// amount rather than on whatever a trade happened to produce.
func (k Keeper) ApplyVolumeForTest(ctx context.Context, pool *types.Pool, erthAmount math.Int) error {
	return k.applyVolume(ctx, pool, erthAmount, sdk.UnwrapSDKContext(ctx).BlockTime())
}
