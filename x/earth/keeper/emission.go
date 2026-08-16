package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

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

// MintAndCompound emits the investor pillar for the elapsed time and compounds
// it straight into bonded stake. It returns the amount minted.
//
// The rate is fixed per *second*, not per block, so it is prorated against the
// previous emission's timestamp — block times vary and the schedule should not.
func (k Keeper) MintAndCompound(ctx context.Context, mintDenom string) (math.Int, error) {
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
	if err := k.compoundIntoStake(ctx, minted); err != nil {
		return math.ZeroInt(), err
	}
	return minted, nil
}

// compoundIntoStake adds freshly minted ERTH to bonded validators' token pots
// without issuing shares.
//
// A delegation is denominated in shares and valued as shares/totalShares *
// tokens, so growing the pot alone raises every delegator's stake
// proportionally — with no per-delegator state, and nothing left pending for
// anyone to claim. That is what removes the need for F1-style reward accounting
// rather than reimplementing it: F1 exists to track *unclaimed* rewards fairly
// across delegation changes, and here there are none.
//
// Commission is withheld before the share-free addition, because afterwards the
// tokens belong to the delegators and cannot be reclaimed.
//
// The coins backing the addition must land in the bonded pool. Staking's
// invariant is that the bonded pool balance equals the sum of bonded validators'
// tokens: validator.Tokens is only the ledger entry describing the pot, and the
// coins are the pot itself. Raising one without funding the other leaves stake
// that cannot be unbonded — and with no crisis module wired, nothing would
// report it until a user tried to withdraw.
func (k Keeper) compoundIntoStake(ctx context.Context, minted math.Int) error {
	if !minted.IsPositive() {
		return nil
	}

	denom, err := k.stakingKeeper.BondDenom(ctx)
	if err != nil {
		return err
	}

	// Read the total before touching anyone: it is the denominator for every
	// slice, and what the compounding index is advanced against.
	totalBonded, err := k.stakingKeeper.TotalBondedTokens(ctx)
	if err != nil {
		return err
	}
	if !totalBonded.IsPositive() {
		return nil // nothing bonded yet; the mint stays put rather than vanishing
	}

	validators, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return err
	}
	if len(validators) == 0 {
		return nil
	}

	type share struct {
		validator  stakingtypes.Validator
		toPot      math.Int
		commission math.Int
	}
	shares := make([]share, 0, len(validators))
	distributed := math.ZeroInt()

	for _, v := range validators {
		slice := minted.Mul(v.Tokens).Quo(totalBonded)
		if !slice.IsPositive() {
			continue
		}
		distributed = distributed.Add(slice)
		shares = append(shares, share{validator: v, toPot: slice, commission: math.ZeroInt()})
	}
	if len(shares) == 0 {
		return nil
	}

	// Truncation leaves up to one unit per validator undistributed each block,
	// which would strand a slow drip on the module account forever. The remainder
	// goes to the largest validator, who is first in power order.
	if remainder := minted.Sub(distributed); remainder.IsPositive() {
		shares[0].toPot = shares[0].toPot.Add(remainder)
	}

	totalToPot, totalCommission := math.ZeroInt(), math.ZeroInt()
	for i := range shares {
		rate := shares[i].validator.Commission.CommissionRates.Rate
		if !rate.IsNil() && rate.IsPositive() {
			shares[i].commission = math.LegacyNewDecFromInt(shares[i].toPot).Mul(rate).TruncateInt()
			shares[i].toPot = shares[i].toPot.Sub(shares[i].commission)
		}
		totalToPot = totalToPot.Add(shares[i].toPot)
		totalCommission = totalCommission.Add(shares[i].commission)
	}

	// Fund the bonded pool before raising any validator's Tokens, so the invariant
	// never observes an intermediate state where the two disagree.
	if totalToPot.IsPositive() {
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName,
			stakingtypes.BondedPoolName, sdk.NewCoins(sdk.NewCoin(denom, totalToPot))); err != nil {
			return err
		}
	}

	for _, s := range shares {
		if s.toPot.IsPositive() {
			// The sequence RemoveValidatorTokens uses for slashing, inverted: the
			// power index is keyed by token count, so it has to be dropped before the
			// mutation and rebuilt after.
			if err := k.stakingKeeper.DeleteValidatorByPowerIndex(ctx, s.validator); err != nil {
				return err
			}
			s.validator.Tokens = s.validator.Tokens.Add(s.toPot)
			if err := k.stakingKeeper.SetValidator(ctx, s.validator); err != nil {
				return err
			}
			if err := k.stakingKeeper.SetValidatorByPowerIndex(ctx, s.validator); err != nil {
				return err
			}
		}

		if s.commission.IsPositive() {
			valAddr, err := sdk.ValAddressFromBech32(s.validator.GetOperator())
			if err != nil {
				return err
			}
			if err := k.AccrueCommission(ctx, valAddr.Bytes(), s.commission); err != nil {
				return err
			}
		}
	}

	// Only the share-free portion grew delegations uniformly. Commission grows one
	// validator's self-delegation later, through a real delegation that marks
	// itself to market, so counting it here would advance the index past the
	// growth that actually happened.
	return k.AdvanceStakeCompoundIndex(ctx, totalToPot, totalBonded)
}
