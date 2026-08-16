package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"

	earthkeeper "github.com/earth-network/earth/x/earth/keeper"
)

// ProvideEarthMintFn overrides x/mint's bonded-ratio inflation with the earth
// module's fixed per-second emission.
//
// The emission does not go to the fee collector. x/earth compounds it directly
// into bonded stake, so there is nothing for a delegator to claim and realising
// it requires unbonding. A consequence worth knowing: with no inflow,
// x/distribution has nothing to allocate, so MsgWithdrawDelegatorReward and
// MsgWithdrawValidatorCommission return nothing rather than needing to be
// blocked. Gas fees still reach the fee collector and are still burned, by the
// earth EndBlocker.
//
// This wrapper exists only because x/mint owns the per-block hook; all the logic
// lives in x/earth, which owns tokenomics.
func ProvideEarthMintFn(earthKeeper earthkeeper.Keeper) mintkeeper.MintFn {
	return func(ctx sdk.Context, k *mintkeeper.Keeper) error {
		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}
		_, err = earthKeeper.MintAndCompound(ctx, params.MintDenom)
		return err
	}
}
