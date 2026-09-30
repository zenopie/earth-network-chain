package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// The pool's one accounting rule: for every denom, the module account's bank
// balance equals the turnstile's In - Out. Exact, in both directions, the
// same as x/dex's solvency check and for the same reason. A shortfall is a
// note that cannot be unshielded. A surplus is a coin that entered without
// being counted — and since notes are hidden, the only thing that stops a
// bug minting unbacked notes from being silent is that the coins behind
// every note entered through a counted path.
//
// Equality is enforceable because nothing but this keeper can move coins
// into the account (SendRestriction).

func (k Keeper) checkDenom(ctx context.Context, denom string) error {
	t, err := k.turnstile(ctx, denom)
	if err != nil {
		return err
	}
	held := k.bankKeeper.GetBalance(ctx, k.poolAddr, denom).Amount
	if !held.Equal(t.Held()) {
		return types.ErrInvariant.Wrapf("%s: pool holds %s, turnstile says %s (in %s, out %s)",
			denom, held, t.Held(), t.In, t.Out)
	}
	return nil
}

// AssertInvariants checks every turnstile against the bank and every coin the
// pool holds against a turnstile. It walks all denoms, so it runs off the
// block path (genesis, tests); EndBlock checks only the denoms that moved.
func (k Keeper) AssertInvariants(ctx context.Context) error {
	seen := map[string]bool{}
	if err := k.Turnstiles.Walk(ctx, nil, func(denom string, _ types.Turnstile) (bool, error) {
		seen[denom] = true
		return false, k.checkDenom(ctx, denom)
	}); err != nil {
		return err
	}
	for _, c := range k.bankKeeper.GetAllBalances(ctx, k.poolAddr) {
		if !seen[c.Denom] {
			return types.ErrInvariant.Wrapf("pool holds %s with no turnstile", c)
		}
	}
	return nil
}

// assertMoved checks the denoms whose turnstile moved this block, and clears
// the set. Sound because only this keeper can move the pool's coins, and it
// marks every denom it moves.
func (k Keeper) assertMoved(ctx context.Context) error {
	var denoms []string
	if err := k.DirtyDenoms.Walk(ctx, nil, func(d string) (bool, error) {
		denoms = append(denoms, d)
		return false, nil
	}); err != nil {
		return err
	}
	for _, d := range denoms {
		if err := k.checkDenom(ctx, d); err != nil {
			sdk.UnwrapSDKContext(ctx).Logger().Error("shielded invariant broken — halting", "err", err,
				"height", sdk.UnwrapSDKContext(ctx).BlockHeight())
			return err
		}
		if err := k.DirtyDenoms.Remove(ctx, d); err != nil {
			return err
		}
	}
	return nil
}
