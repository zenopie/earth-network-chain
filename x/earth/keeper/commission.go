package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/earth/types"
)

// Validator commission under the compounding emission.
//
// The emission is added to a validator's token pot without issuing shares, so
// every delegator's stake grows proportionally for free. That mechanism pays a
// validator nothing beyond the growth on their own self-delegation — it is a 0%
// commission chain by default. Commission therefore has to be *withheld* before
// the share-free addition and parked here, because once the remainder is in the
// pot it belongs to the delegators and cannot be reclaimed.
//
// What is parked here is real minted ERTH held on the module account. It is not
// a promise: circulating supply matches the emission schedule at every block,
// and MsgCompoundCommission moves it into the validator's self-delegation.

// GetAccruedCommission returns a validator's withheld commission, zero if none.
func (k Keeper) GetAccruedCommission(ctx context.Context, valAddr []byte) (math.Int, error) {
	v, err := k.AccruedCommission.Get(ctx, valAddr)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

// AccrueCommission adds to a validator's withheld commission.
func (k Keeper) AccrueCommission(ctx context.Context, valAddr []byte, amount math.Int) error {
	if !amount.IsPositive() {
		return nil
	}
	cur, err := k.GetAccruedCommission(ctx, valAddr)
	if err != nil {
		return err
	}
	return k.AccruedCommission.Set(ctx, valAddr, cur.Add(amount))
}

// SlashAccruedCommission reduces a validator's withheld commission by the same
// fraction their stake is being slashed by.
//
// Without this, commission would be the one pot of a validator's earnings that
// misbehaviour cannot touch — and since a slash falls on the whole delegation
// pool, an operator with a thin self-bond already externalises most of the cost
// onto their delegators. Exempting the commission on top would make it cheaper
// still. It is exposed to exactly the risk that earned it.
func (k Keeper) SlashAccruedCommission(ctx context.Context, valAddr []byte, fraction math.LegacyDec) error {
	if fraction.IsNil() || !fraction.IsPositive() {
		return nil
	}
	cur, err := k.GetAccruedCommission(ctx, valAddr)
	if err != nil || !cur.IsPositive() {
		return err
	}

	if fraction.GTE(math.LegacyOneDec()) {
		return k.burnAccruedCommission(ctx, valAddr, cur)
	}
	remaining := math.LegacyNewDecFromInt(cur).Mul(math.LegacyOneDec().Sub(fraction)).TruncateInt()
	if err := k.burnAccruedCommission(ctx, valAddr, cur.Sub(remaining)); err != nil {
		return err
	}
	if !remaining.IsPositive() {
		return k.AccruedCommission.Remove(ctx, valAddr)
	}
	return k.AccruedCommission.Set(ctx, valAddr, remaining)
}

// ForfeitAccruedCommission destroys a validator's withheld commission outright.
// Used on tombstoning: there is no longer a validator to self-bond into, and
// leaving a liquid escape hatch would make getting tombstoned a way to convert
// pending commission into spendable tokens.
func (k Keeper) ForfeitAccruedCommission(ctx context.Context, valAddr []byte) error {
	cur, err := k.GetAccruedCommission(ctx, valAddr)
	if err != nil || !cur.IsPositive() {
		return err
	}
	if err := k.burnAccruedCommission(ctx, valAddr, cur); err != nil {
		return err
	}
	return k.AccruedCommission.Remove(ctx, valAddr)
}

// burnAccruedCommission destroys coins the module is holding against a ledger
// entry, keeping the module balance equal to the sum of the ledger.
func (k Keeper) burnAccruedCommission(ctx context.Context, _ []byte, amount math.Int) error {
	if !amount.IsPositive() {
		return nil
	}
	denom, err := k.stakingKeeper.BondDenom(ctx)
	if err != nil {
		return err
	}
	return k.bankKeeper.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(sdk.NewCoin(denom, amount)))
}
