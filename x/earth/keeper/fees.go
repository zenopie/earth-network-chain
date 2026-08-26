package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/earth/types"
)

// two is the fee split denominator. Named so the halving reads as the deliberate
// shape it is rather than as a magic number.
var two = math.NewInt(2)

// SplitCollectedFees halves the transaction fees this block collected: one half
// is burned, the other is left for the validators who produced the block.
//
// Both halves are doing a job, and neither alone is enough:
//
//   - Burning makes gas deflationary, so using the chain retires supply rather
//     than merely moving it around.
//   - Paying validators is the only part of their revenue that responds to
//     anything. The staking pillar is a fixed 1 ERTH/sec split by voting power,
//     so it falls per-validator as validators join and stays flat no matter how
//     much load the chain carries. Fees are the one term that grows with use.
//
// That second point is sharper for this chain than for most. Verifying a
// registration proof costs real CPU — it is why proof_verification_gas exists —
// and validators bear that cost on every registration. Burning the whole fee
// would mean the chain's flagship operation is simultaneously its most expensive
// to validate and its least rewarded.
//
// A fifty-fifty split, and not a parameter, for the same reason the swap fee is
// split fifty-fifty in splitFee (x/dex/keeper/amm.go): it is a shape, not a
// tuning. How much a fee is worth is already decided elsewhere and by someone
// else — each validator sets the minimum gas price they will accept in their own
// app.toml, and the market between them finds the level. Making the ratio a
// governance dial would add a second, centralised answer to a question the
// validator set already answers individually.
//
// Mechanism: the burned half is moved out and destroyed, and the rest is simply
// left where it is. x/distribution's BeginBlocker sweeps the whole fee collector
// on the next block and pays it out under the standard rules — commission,
// community tax, MsgWithdrawDelegatorReward — so nothing here duplicates the
// SDK's payout path.
//
// Timing: this runs in EndBlock, and by then the fee collector holds only this
// block's gas. The emission passes through the same account but is minted by
// x/mint's BeginBlocker and swept by x/distribution in that same BeginBlock
// (mint is ordered before distribution), so it is long gone before this runs and
// the staking emission is never at risk of being burned here.
func (k Keeper) SplitCollectedFees(ctx context.Context) error {
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	fees := k.bankKeeper.SpendableCoins(ctx, feeCollector)
	if fees.IsZero() {
		return nil
	}

	// Halve each denom separately, rounding the burn UP so the odd unit is
	// destroyed rather than paid out. burned + paid == collected exactly, the
	// same convention splitFee uses on the swap fee: where a split cannot be
	// even, the supply reduction wins the remainder.
	burn := sdk.NewCoins()
	for _, fee := range fees {
		if half := fee.Amount.Add(math.OneInt()).Quo(two); half.IsPositive() {
			burn = burn.Add(sdk.NewCoin(fee.Denom, half))
		}
	}
	if burn.IsZero() {
		return nil
	}

	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, authtypes.FeeCollectorName, types.ModuleName, burn); err != nil {
		return err
	}
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, burn); err != nil {
		return err
	}
	if err := k.RecordBurn(ctx, types.SourceGasFees, burn); err != nil {
		return err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent("gas_fees_split",
			sdk.NewAttribute("burned", burn.String()),
			sdk.NewAttribute("to_validators", fees.Sub(burn...).String()),
		),
	)
	return nil
}
