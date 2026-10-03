package keeper

import (
	"context"
	"math/big"
	"strconv"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	"github.com/earth-network/earth/x/dex/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// Retiring protocol-owned liquidity.
//
// The chain starts owning all of its own liquidity: the ANML/ERTH pool's shares
// are minted to the dex module account at genesis, and the auction pool's are
// minted there when it settles. That account has no key, so nothing can withdraw
// them — which used to mean the positions were permanent.
//
// They are not. Each is retired on a straight line over PolBurnSeconds, because
// running a book is active management and the protocol is a bad manager of it:
// an LP's incentives are not an ERTH staker's, and a position nobody can adjust
// is the worst version of that mismatch. Retiring it makes room for providers
// who will actually manage the liquidity, and gives them five years of steadily
// rising reward share as the reason to show up.
//
// Retirement is not a withdrawal. A slice of the position is priced against the
// reserves exactly as a real withdrawal would be, and then destroyed: the shares
// are burned, and so are the assets behind them. Because both sides shrink by
// the same fraction, an ANML/ERTH tranche does not move the pool's price at all.
//
// The auction pool is the exception (burn_token = false). Its spoke side is a
// bridged asset the chain cannot recreate, so only the ERTH is burned and the
// spoke asset stays in the reserve. See PolBurn in pool.proto for why that ends
// up spending the auction's proceeds on buying ERTH to burn.

// BurnDuePol advances every live retirement schedule to the current block time.
//
// It walks the schedules rather than indexing them by due time, which is only
// safe because there are two of them and no message can create a third.
func (k Keeper) BurnDuePol(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()

	var due []types.PolBurn
	if err := k.PolBurns.Walk(ctx, nil, func(_ uint64, b types.PolBurn) (stop bool, err error) {
		due = append(due, b)
		return false, nil
	}); err != nil {
		return err
	}

	// Each schedule on its own branch, recovering panics. A failure skips the
	// schedule for this block and is retried on the next: the target is
	// recomputed from the clock (retirableAt), so a skipped block loses
	// nothing, and a halt from the EndBlocker would be strictly worse.
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, b := range due {
		if err := safeexec.Cached(sdkCtx, func(cache sdk.Context) error {
			return k.advancePolBurn(cache, b, now)
		}); err != nil {
			sdkCtx.Logger().Error("pol retirement failed; retrying next block", "pool_id", b.PoolId, "err", err)
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				"burn_pol_failed",
				sdk.NewAttribute("pool_id", strconv.FormatUint(b.PoolId, 10)),
				sdk.NewAttribute("error", err.Error()),
			))
		}
	}
	return nil
}

// retirableAt returns how many shares the schedule should have retired in total
// by `now`, measured from its start.
//
// The target is recomputed from the clock every block instead of being
// accumulated tranche by tranche. Truncation therefore never compounds, and a
// chain that halts for a week does not push the end date a week later: it
// catches up in the block it resumes on.
//
// The same catch-up applies to a chain launched from a stale genesis_time, and
// it is worth knowing before it surprises someone. CometBFT stamps block 1 with
// genesis_time and gives block 2 the wall clock, so a genesis file four days old
// anchors the schedule four days in the past and retires four days of the
// position in a single block. This is the emission's failure mode exactly (see
// docker/README.md on 125,485 ERTH minted at height 2) and it has the
// same fix: launch from a genesis_time close to when the chain actually starts.
func retirableAt(b types.PolBurn, now int64) math.Int {
	if b.DurationSeconds <= 0 || now <= b.StartTime {
		return math.ZeroInt()
	}
	elapsed := now - b.StartTime
	if elapsed >= b.DurationSeconds {
		return b.TotalShares
	}
	// big.Int: TotalShares*elapsed can pass 256 bits for a schedule over a
	// share supply near the type's range, and MulRaw panics there.
	q := new(big.Int).Mul(b.TotalShares.BigInt(), big.NewInt(elapsed))
	return math.NewIntFromBigInt(q.Quo(q, big.NewInt(b.DurationSeconds)))
}

// advancePolBurn retires whatever the clock says is owed on one schedule.
func (k Keeper) advancePolBurn(ctx context.Context, b types.PolBurn, now int64) error {
	// A schedule written into genesis cannot know the chain's first block time,
	// so a zero start means "start here". Anchoring it on the first block that
	// sees the entry — rather than treating zero as the unix epoch — is what
	// keeps the whole position from being retired in block one.
	if b.StartTime == 0 {
		b.StartTime = now
		return k.PolBurns.Set(ctx, b.PoolId, b)
	}

	retired := b.TotalShares.Sub(b.SharesRemaining)
	slice := retirableAt(b, now).Sub(retired)
	if slice.GT(b.SharesRemaining) {
		slice = b.SharesRemaining
	}
	if !slice.IsPositive() {
		return nil
	}

	burnedErth, burnedToken, err := k.retirePolShares(ctx, b, slice)
	if err != nil {
		return err
	}

	b.SharesRemaining = b.SharesRemaining.Sub(slice)
	if b.SharesRemaining.IsZero() {
		// The position is gone; keeping a finished schedule around would only be
		// something for the walk above to reload every block.
		if err := k.PolBurns.Remove(ctx, b.PoolId); err != nil {
			return err
		}
	} else if err := k.PolBurns.Set(ctx, b.PoolId, b); err != nil {
		return err
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"burn_pol",
			sdk.NewAttribute("pool_id", strconv.FormatUint(b.PoolId, 10)),
			sdk.NewAttribute("shares", slice.String()),
			sdk.NewAttribute("shares_remaining", b.SharesRemaining.String()),
			sdk.NewAttribute("burned_erth", burnedErth.String()),
			sdk.NewAttribute("burned_token", burnedToken.String()),
		),
	)
	return nil
}

// retirePolShares prices `slice` against the pool and destroys it.
func (k Keeper) retirePolShares(ctx context.Context, b types.PolBurn, slice math.Int) (sdk.Coin, sdk.Coin, error) {
	pool, err := k.Pool.Get(ctx, b.PoolId)
	if err != nil {
		return sdk.Coin{}, sdk.Coin{}, err
	}
	// Compound pending rewards into the reserve before pricing against it. The
	// position held these shares the whole interval, so the ERTH they earned is
	// part of what this slice owns — and burning it back out is the point.
	if err := k.settlePoolRewards(ctx, b.PoolId, &pool); err != nil {
		return sdk.Coin{}, sdk.Coin{}, err
	}

	// Every outstanding share is in the denominator, including shares escrowed
	// against a third party's in-flight unbonding, for the same reason the
	// unbonding payout counts them: they still own their slice of the pool.
	total := k.totalShares(ctx, b.PoolId).Amount
	if !total.IsPositive() || slice.GT(total) {
		return sdk.Coin{}, sdk.Coin{}, types.ErrInsufficientPool.Wrapf(
			"pool %d has %s shares outstanding against a pol retirement of %s",
			b.PoolId, total, slice)
	}

	// big.Int, as in payoutUnbonding: math.Int's Mul panics past 256 bits.
	erthAmt, err := mulDiv(slice, pool.ReserveErth.Amount, total)
	if err != nil {
		return sdk.Coin{}, sdk.Coin{}, types.ErrInsufficientPool.Wrapf("pool %d: %s", b.PoolId, err)
	}
	outErth := sdk.NewCoin(pool.ReserveErth.Denom, erthAmt)
	outToken := sdk.NewCoin(pool.ReserveToken.Denom, math.ZeroInt())
	if b.BurnToken {
		if outToken.Amount, err = mulDiv(slice, pool.ReserveToken.Amount, total); err != nil {
			return sdk.Coin{}, sdk.Coin{}, types.ErrInsufficientPool.Wrapf("pool %d: %s", b.PoolId, err)
		}
	}

	burn := sdk.NewCoins(sdk.NewCoin(types.LPShareDenom(b.PoolId), slice), outErth, outToken)
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, burn); err != nil {
		return sdk.Coin{}, sdk.Coin{}, err
	}
	// The shares are deliberately not counted. Burning them destroys a claim on
	// the pool, not supply — every withdrawal does the same — and totalling them
	// alongside the assets would inflate the figure with an accounting entry.
	if err := k.burnRecorder.RecordBurn(ctx, earthtypes.SourcePolRetire,
		sdk.NewCoins(outErth, outToken)); err != nil {
		return sdk.Coin{}, sdk.Coin{}, err
	}

	pool.ReserveErth = pool.ReserveErth.Sub(outErth)
	pool.ReserveToken = pool.ReserveToken.Sub(outToken)
	if err := k.SetPool(ctx, b.PoolId, pool); err != nil {
		return sdk.Coin{}, sdk.Coin{}, err
	}

	return outErth, outToken, nil
}
