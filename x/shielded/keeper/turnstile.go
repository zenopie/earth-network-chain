package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/shielded/types"
)

// Every coin that enters the pool is counted into In for its denom and every
// coin that leaves into Out, in the same call that moves it. The module's
// balance of the denom is In - Out at all times; EndBlock checks the denoms
// that moved and halts on a mismatch (see invariants.go).

func (k Keeper) turnstile(ctx context.Context, denom string) (types.Turnstile, error) {
	t, err := k.Turnstiles.Get(ctx, denom)
	if errors.Is(err, collections.ErrNotFound) {
		return types.Turnstile{Denom: denom, In: math.ZeroInt(), Out: math.ZeroInt()}, nil
	}
	return t, err
}

func (k Keeper) countIn(ctx context.Context, denom string, amt math.Int) error {
	t, err := k.turnstile(ctx, denom)
	if err != nil {
		return err
	}
	t.In = t.In.Add(amt)
	if err := k.Turnstiles.Set(ctx, denom, t); err != nil {
		return err
	}
	return k.DirtyDenoms.Set(ctx, denom)
}

func (k Keeper) countOut(ctx context.Context, denom string, amt math.Int) error {
	t, err := k.turnstile(ctx, denom)
	if err != nil {
		return err
	}
	t.Out = t.Out.Add(amt)
	if t.Out.GT(t.In) {
		return types.ErrInvariant.Wrapf("%s would leave the pool more than entered it", denom)
	}
	if err := k.Turnstiles.Set(ctx, denom, t); err != nil {
		return err
	}
	return k.DirtyDenoms.Set(ctx, denom)
}

// Turnstile returns denom's counters (zero if nothing ever entered).
func (k Keeper) Turnstile(ctx context.Context, denom string) (types.Turnstile, error) {
	return k.turnstile(ctx, denom)
}
