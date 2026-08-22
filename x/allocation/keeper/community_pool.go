package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// CommunityPoolHandler resolves the capital stream's emergency-fund option by
// minting its accrued ERTH and crediting it to the SDK community pool.
//
// It is a constructor returning a handler rather than a method on Keeper because
// the collaborator is x/distribution, an SDK module that cannot register itself
// into this keeper the way x/dex registers lp_rewards. Holding the distribution
// keeper as a Keeper field instead would close a depinject cycle: distribution
// needs staking, staking collects this module's staking hooks, so this module
// cannot in turn ask for distribution. The app wires it up after the container
// is built — see app/app.go.
//
// The whole accrual resolves every block, so nothing is ever left pending: the
// mint and the credit are exact, with no index truncation to carry forward the
// way lp_rewards has.
func CommunityPoolHandler(k Keeper, pool types.CommunityPoolKeeper) IntegratedHandler {
	// The module account pays as an ordinary sender. FundCommunityPool uses
	// SendCoinsFromAccountToModule, which does not consult the blocked-address
	// list, so this reaches the distribution account that a claim payout could
	// not.
	sender := sdk.AccAddress(authtypes.NewModuleAddress(types.ModuleName))

	return func(ctx context.Context, accrued math.Int) (math.Int, error) {
		if !accrued.IsPositive() {
			return math.ZeroInt(), nil
		}
		denom, err := k.HubDenom(ctx)
		if err != nil {
			return math.ZeroInt(), err
		}
		coins := sdk.NewCoins(sdk.NewCoin(denom, accrued))
		if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, coins); err != nil {
			return math.ZeroInt(), err
		}
		if err := pool.FundCommunityPool(ctx, coins, sender); err != nil {
			return math.ZeroInt(), err
		}
		return accrued, nil
	}
}
