package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/earth/types"
)

// GetLastMintTime returns the previous emission's block time (unix nanos), or 0.
func (k Keeper) GetLastMintTime(ctx context.Context) (int64, error) {
	t, err := k.LastMintTime.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return t, nil
}

// MintEmission emits the investor pillar for the elapsed time and hands it to
// the fee collector, which is where x/distribution expects to find a block's
// staking rewards. It returns the amount minted.
//
// From there the emission follows the SDK's normal rules: x/distribution splits
// it by voting power, withholds each validator's commission at their configured
// rate, takes the community tax, and leaves the rest claimable through
// MsgWithdrawDelegatorReward and MsgWithdrawValidatorCommission.
//
// The rate is fixed per *second*, not per block, so it is prorated against the
// previous emission's timestamp — block times vary and the schedule should not.
// That is the only part of issuance this chain does differently: how much is
// minted, never who may claim it or on what terms.
func (k Keeper) MintEmission(ctx context.Context, mintDenom string) (math.Int, error) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	last, err := k.GetLastMintTime(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	if err := k.LastMintTime.Set(ctx, now); err != nil {
		return math.ZeroInt(), err
	}
	// First block (no previous timestamp) or non-increasing time: mint nothing.
	if last == 0 || now <= last {
		return math.ZeroInt(), nil
	}

	minted := math.NewInt(types.EmissionPerSecondPerPillar).
		MulRaw(now - last).QuoRaw(int64(time.Second))
	if !minted.IsPositive() {
		return math.ZeroInt(), nil
	}

	coins := sdk.NewCoins(sdk.NewCoin(mintDenom, minted))
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, coins); err != nil {
		return math.ZeroInt(), err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToModule(
		ctx, types.ModuleName, authtypes.FeeCollectorName, coins,
	); err != nil {
		return math.ZeroInt(), err
	}
	return minted, nil
}
