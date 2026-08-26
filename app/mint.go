package app

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"

	earthkeeper "github.com/earth-network/earth/x/earth/keeper"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// secondsPerYear is the 365-day year the pillars are sized against: a quarter of
// the pre-mine is 630,720,000 ERTH, which is 1 ERTH/sec across five of these.
// Using 365.25 here would report an issuance rate the token supply was never
// built from.
const secondsPerYear = 365 * 24 * 60 * 60

// ProvideEarthMintFn overrides x/mint's bonded-ratio inflation with the earth
// module's fixed per-second emission.
//
// Only the amount changes. Like the default mint function, this pays the newly
// minted coins into the fee collector, so x/distribution splits them by voting
// power under the standard rules and delegators claim with
// MsgWithdrawDelegatorReward as they would on any other Cosmos chain. Gas fees
// also reach the fee collector, and are split by the earth EndBlocker — half
// burned, half left for the next block's distribution sweep — after distribution
// has already taken the emission.
//
// This wrapper exists only because x/mint owns the per-block hook; all the logic
// lives in x/earth, which owns tokenomics.
func ProvideEarthMintFn(earthKeeper earthkeeper.Keeper) mintkeeper.MintFn {
	return func(ctx sdk.Context, k *mintkeeper.Keeper) error {
		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}
		if _, err := earthKeeper.MintEmission(ctx, params.MintDenom); err != nil {
			return err
		}
		return publishInflation(ctx, k)
	}
}

// publishInflation writes this chain's issuance into x/mint's Minter, which
// nothing here reads and every third party does.
//
// Overriding the mint function left the Minter at its zero value, so
// /cosmos/mint/v1beta1/inflation and .../annual_provisions both answered
// 0.000000000000000000. Wallets and explorers derive staking yield and monetary
// policy from those two fields, and a chain issuing 126,144,000 ERTH a year was
// telling all of them it issued nothing. The app itself is unaffected: both
// clients compute from the fixed per-second rate directly and never ask.
//
// What is published is GROSS issuance, which is what these fields mean —
// AnnualProvisions = TotalSupply * Inflation is the SDK's own identity, and it
// describes minting, not the change in supply. This chain also burns, currently
// faster than it mints, so its supply is falling while this figure is positive.
// That is not expressible here: ValidateMinter rejects a negative inflation, so
// there is no honest net figure to publish even if the field's meaning allowed
// one. Net belongs to /earth/earth/v1/burns, which reports both sides.
func publishInflation(ctx sdk.Context, k *mintkeeper.Keeper) error {
	minter, err := k.Minter.Get(ctx)
	if err != nil {
		return err
	}

	supply, err := k.StakingTokenSupply(ctx)
	if err != nil {
		return err
	}
	// A chain with no supply has no rate to express, and dividing by it would
	// panic. Genesis always funds accounts, so this is unreachable in practice
	// and guarded anyway.
	if !supply.IsPositive() {
		return nil
	}

	annual, inflation := grossIssuance(supply)
	minter.AnnualProvisions = annual
	minter.Inflation = inflation

	return k.Minter.Set(ctx, minter)
}

// grossIssuance is the pair x/mint publishes: what this chain mints in a year,
// and that as a fraction of the supply it is minted into.
//
// The denominator is TOTAL supply, not bonded — that is what the SDK's
// AnnualProvisions = TotalSupply * Inflation identity means, and the difference
// is not small: against bonded stake this same numerator would read a rate in
// the thousands of percent.
//
// Note the direction this moves. A fixed numerator over a growing supply falls,
// which is the decay x/earth/types/keys.go describes. While the protocol-owned
// liquidity is retiring, supply shrinks instead, so this figure RISES for those
// five years and only begins falling once the retirement is spent. That is the
// arithmetic being honest about a denominator that goes down before it goes up.
func grossIssuance(supply math.Int) (annual, inflation math.LegacyDec) {
	annual = math.LegacyNewDecFromInt(
		math.NewInt(earthtypes.TotalEmissionPerSecond).MulRaw(secondsPerYear),
	)
	return annual, annual.QuoInt(supply)
}
