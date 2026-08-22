package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// This file is the chain's price oracle: a Uniswap-v2 style cumulative price
// accumulator, and the reads built on top of it.
//
// The problem it solves is that a pool's spot price is whatever the last trade
// left behind, so anything that prices off it can be moved by the trade
// immediately before it. Averaging over time fixes that, but storing a price
// history per pool does not scale. The accumulator does: it holds the running
// sum of price * seconds-at-that-price, so the average across any two readings
// is their difference over the seconds between them. One value per pool covers
// every window, and moving the average requires holding the price away from it
// for real time rather than for one block.

// spotPrice returns a pool's instantaneous price in ERTH per unit of the spoke
// token. Zero on either side means the pool cannot be priced at all.
func spotPrice(pool types.Pool) (math.LegacyDec, bool) {
	erth, token := pool.ReserveErth.Amount, pool.ReserveToken.Amount
	if erth.IsNil() || token.IsNil() || !erth.IsPositive() || !token.IsPositive() {
		return math.LegacyDec{}, false
	}
	return math.LegacyNewDecFromInt(erth).Quo(math.LegacyNewDecFromInt(token)), true
}

// getPriceCumulative returns a pool's stored accumulator and the second it was
// last brought forward. A pool that has never been accumulated reads as zero at
// time zero, which advancePriceCumulative treats as "start the clock here".
func (k Keeper) getPriceCumulative(ctx context.Context, poolID uint64) (math.LegacyDec, int64, error) {
	cum, err := k.PriceCumulative.Get(ctx, poolID)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return math.LegacyDec{}, 0, err
		}
		cum = math.LegacyZeroDec()
	}
	at, err := k.PriceObservedAt.Get(ctx, poolID)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return math.LegacyDec{}, 0, err
		}
		at = 0
	}
	return cum, at, nil
}

// advancePriceCumulative credits the pool's current price for the time it has
// been in effect and stamps the accumulator to now.
//
// Callers must invoke it BEFORE they change the reserves. The price being
// credited is the one that actually held over the elapsed interval, which is the
// pre-trade price; crediting the post-trade price would attribute a new price to
// time that had already passed, and would let a single swap write its own price
// into the average retroactively — defeating the point of averaging.
func (k Keeper) advancePriceCumulative(ctx context.Context, poolID uint64, pool types.Pool) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	cum, at, err := k.getPriceCumulative(ctx, poolID)
	if err != nil {
		return err
	}
	price, ok := spotPrice(pool)
	if !ok {
		// An unpriceable pool contributes nothing, but the clock still moves:
		// leaving the stamp behind would later credit this empty stretch to
		// whatever price the pool eventually has.
		return k.PriceObservedAt.Set(ctx, poolID, now)
	}
	// at == 0 is the pool's first observation: there is no interval behind it to
	// credit, so this only starts the clock.
	if at != 0 && now > at {
		cum = cum.Add(price.MulInt64(now - at))
		if err := k.PriceCumulative.Set(ctx, poolID, cum); err != nil {
			return err
		}
	} else if at == 0 {
		if err := k.PriceCumulative.Set(ctx, poolID, cum); err != nil {
			return err
		}
	}
	return k.PriceObservedAt.Set(ctx, poolID, now)
}

// TwapObservation returns a reading of the price accumulator for a token's pool,
// together with the block second it was taken at, and the pool's current spot
// price.
//
// It is the whole public surface of the oracle. A consumer stores one reading,
// takes another later, and divides the difference by the seconds between them to
// get the average price over that stretch; the spot price comes back alongside
// so the consumer can also ask how far the pool has been pushed from that
// average right now.
func (k Keeper) TwapObservation(ctx context.Context, tokenDenom string) (cumulative, spot math.LegacyDec, observedAt int64, err error) {
	pool, err := k.PoolForToken(ctx, tokenDenom)
	if err != nil {
		return math.LegacyDec{}, math.LegacyDec{}, 0, errorsmod.Wrapf(types.ErrPoolNotFound, "no pool for %s", tokenDenom)
	}
	price, ok := spotPrice(pool)
	if !ok {
		return math.LegacyDec{}, math.LegacyDec{}, 0, errorsmod.Wrapf(types.ErrInsufficientPool, "pool for %s has no price", tokenDenom)
	}
	// Reading the oracle brings it up to date and persists it, which is not the
	// side effect it looks like — it is what makes the oracle work on a quiet
	// pool. The accumulator is otherwise only written when a swap touches the
	// pool, so a pool nobody trades would report the same value at every reading
	// forever, every difference between two readings would be zero, and the
	// average price of a pool that has simply been sitting still would come out
	// as zero rather than as the price it has been sitting at.
	//
	// Deterministic, so it is safe from consensus' point of view: the value
	// written is a function of the stored reserves and the block time, both of
	// which every validator already agrees on.
	if err := k.advancePriceCumulative(ctx, pool.PoolId, pool); err != nil {
		return math.LegacyDec{}, math.LegacyDec{}, 0, err
	}
	cum, at, err := k.getPriceCumulative(ctx, pool.PoolId)
	if err != nil {
		return math.LegacyDec{}, math.LegacyDec{}, 0, err
	}
	return cum, price, at, nil
}

// QuoteHubToToken reports what an ERTH -> token swap of amountErthIn would
// return right now, without changing anything.
//
// It runs the real swap against a cache context and throws the writes away, so
// the number is produced by the same code the swap will execute rather than by a
// second copy of the pricing maths kept in step by hand. Fee splitting, reward
// compounding into the reserve and rounding are all therefore identical to what
// the trade actually does.
func (k Keeper) QuoteHubToToken(ctx context.Context, tokenDenom string, amountErthIn math.Int) (math.Int, error) {
	if !amountErthIn.IsPositive() {
		return math.Int{}, errorsmod.Wrap(types.ErrInvalidAmount, "amount must be positive")
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return math.Int{}, err
	}
	cacheCtx, _ := sdk.UnwrapSDKContext(ctx).CacheContext() // writes intentionally discarded
	out, _, err := k.hopHubToToken(cacheCtx, tokenDenom, amountErthIn, params.SwapFee)
	if err != nil {
		return math.Int{}, err
	}
	return out, nil
}
