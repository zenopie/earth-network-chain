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

// publishInflation writes this mint function's issuance into x/mint's Minter,
// which nothing here reads and every third party does.
//
// Overriding the mint function left the Minter at its zero value, so
// /cosmos/mint/v1beta1/inflation and .../annual_provisions both answered
// 0.000000000000000000. Wallets and explorers derive staking yield from those
// two fields, and a chain paying stakers 31,536,000 ERTH a year was telling all
// of them it paid nothing. The app itself is unaffected: both clients compute
// from the fixed per-second rate directly and never ask.
//
// What is published is what THIS mint function mints, because that is what the
// SDK means by these fields. DefaultMintFn (x/mint/keeper/mint.go) sets
// AnnualProvisions, mints AnnualProvisions/BlocksPerYear each block, and pays
// all of it to the fee collector — so AnnualProvisions is by construction the
// module's own annual issuance, and Inflation is that over the staking token
// supply. Every SDK-derived client is built against that identity; publishing a
// larger figure here reads to them as a proportionally larger staking APR.
//
// That makes this the investor pillar alone. The chain's GROSS issuance is four
// times this: the other three pillars are minted by x/personhood and
// x/allocation, never reach the fee collector, and so are not x/mint's to
// report. Those rates are constants in x/earth/types/keys.go and are not yet
// served over RPC — the chain's own endpoint, /earth/earth/v1/burns, covers only
// the destruction side, so gross issuance and net supply change currently have
// no query. Worth adding to x/earth rather than distorting this one.
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

	annual, inflation := stakingIssuance(supply)
	minter.AnnualProvisions = annual
	minter.Inflation = inflation

	return k.Minter.Set(ctx, minter)
}

// stakingIssuance is the pair x/mint publishes: what this mint function mints in
// a year, and that as a fraction of the supply it is minted into.
//
// The numerator is one pillar, matching what MintEmission actually pays into the
// fee collector. The denominator is the staking token supply, matching the SDK's
// NextAnnualProvisions, so that AnnualProvisions = supply * Inflation holds
// exactly as it does on a chain running DefaultMintFn.
//
// Note the direction this moves. A fixed numerator over a growing supply falls,
// which is the decay x/earth/types/keys.go describes. While the protocol-owned
// liquidity is retiring, supply shrinks instead, so this figure RISES for those
// five years and only begins falling once the retirement is spent. That is the
// arithmetic being honest about a denominator that goes down before it goes up.
func stakingIssuance(supply math.Int) (annual, inflation math.LegacyDec) {
	annual = math.LegacyNewDecFromInt(
		math.NewInt(earthtypes.EmissionPerSecondPerPillar).MulRaw(secondsPerYear),
	)
	return annual, annual.QuoInt(supply)
}
