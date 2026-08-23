package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// CommunityPoolHandler resolves the capital stream's emergency-fund option by
// crediting its accrued ERTH to the SDK community pool, and carries the streams'
// truncation residue out with it.
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
// credit is exact, with no index truncation to carry forward the way lp_rewards
// has.
//
// The streams' truncation residue goes to the same place but NOT through here.
// This handler only runs when the option has accrued something, which means only
// when somebody has voted for the emergency fund — so routing the dust through
// it would strand the dust for as long as the fund had no votes, which at launch
// is indefinitely. ResidueSink below is a separate wire for that reason.
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
		// Already minted by AdvanceIndex and sitting in this module's account,
		// so this only moves it. FundCommunityPool must be the mover: a bank
		// transfer alone would raise the distribution account's balance without
		// crediting FeePool, and the coins could never be spent again.
		if err := pool.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(denom, accrued)), sender); err != nil {
			return math.ZeroInt(), err
		}
		return accrued, nil
	}
}

// ResidueSink hands the streams' truncation residue to the community pool.
//
// It is registered separately from CommunityPoolHandler, and the reason is the
// bug that separation fixes: an INTEGRATED handler is only invoked when its
// option has accrued something, so a residue routed through the emergency fund
// would sit in this module's account untouched for as long as nobody voted for
// that fund. At launch nobody has. The dust is not the fund's, and it must not
// depend on the fund's popularity to move.
//
// Same collaborator, same reason it arrives from app.go rather than being held
// on the Keeper: x/distribution cannot be asked for from inside this module
// without closing a cycle through the staking hooks it provides.
func ResidueSink(k Keeper, pool types.CommunityPoolKeeper) func(context.Context, math.Int) error {
	sender := sdk.AccAddress(authtypes.NewModuleAddress(types.ModuleName))
	return func(ctx context.Context, amount math.Int) error {
		denom, err := k.HubDenom(ctx)
		if err != nil {
			return err
		}
		return pool.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(denom, amount)), sender)
	}
}
