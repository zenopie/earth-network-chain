package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"

	earthkeeper "github.com/earth-network/earth/x/earth/keeper"
)

// ProvideEarthMintFn overrides x/mint's bonded-ratio inflation with the earth
// module's fixed per-second emission.
//
// Only the amount changes. Like the default mint function, this pays the newly
// minted coins into the fee collector, so x/distribution splits them by voting
// power under the standard rules and delegators claim with
// MsgWithdrawDelegatorReward as they would on any other Cosmos chain. Gas fees
// also reach the fee collector, and are burned by the earth EndBlocker after
// distribution has already swept the emission.
//
// This wrapper exists only because x/mint owns the per-block hook; all the logic
// lives in x/earth, which owns tokenomics.
func ProvideEarthMintFn(earthKeeper earthkeeper.Keeper) mintkeeper.MintFn {
	return func(ctx sdk.Context, k *mintkeeper.Keeper) error {
		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}
		_, err = earthKeeper.MintEmission(ctx, params.MintDenom)
		return err
	}
}
