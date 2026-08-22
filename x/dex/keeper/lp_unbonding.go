package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// SweepMaturedUnbondings pays out liquidity withdrawals whose unbonding period
// has elapsed, sending the assets straight to the provider's wallet. There is no
// claim message: a provider who starts a withdrawal and never comes back still
// receives it.
//
// Entries are keyed by completion time first, so this walks them in due order
// and stops at the first one that is not ready — the cost is proportional to
// what actually matured, not to how many withdrawals are outstanding. The batch
// is capped because each payout settles a pool and moves two coins; a large
// cohort maturing together would otherwise land unbounded work on one block. The
// remainder is not stranded, since the next block resumes from the oldest entry.
func (k Keeper) SweepMaturedUnbondings(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()

	type matured struct {
		key   collections.Triple[int64, uint64, []byte]
		entry types.LpUnbonding
	}
	due := make([]matured, 0, types.LpUnbondSweepLimit)

	iter, err := k.LpUnbondings.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	capped := false
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		if key.K1() > now {
			break // ordered by completion time: nothing later is due either
		}
		if len(due) == types.LpUnbondSweepLimit {
			capped = true
			break
		}
		entry, err := iter.Value()
		if err != nil {
			iter.Close()
			return err
		}
		due = append(due, matured{key: key, entry: entry})
	}
	iter.Close()

	for _, m := range due {
		if err := k.payoutUnbonding(ctx, m.entry); err != nil {
			return err
		}
		if err := k.LpUnbondings.Remove(ctx, m.key); err != nil {
			return err
		}
	}

	if capped {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
			sdk.NewEvent(
				"lp_unbond_sweep_capped",
				sdk.NewAttribute("limit", strconv.Itoa(types.LpUnbondSweepLimit)),
			),
		)
	}
	return nil
}

// payoutUnbonding prices one matured entry against the pool as it stands now,
// burns the escrowed shares and sends the assets to the provider.
func (k Keeper) payoutUnbonding(ctx context.Context, entry types.LpUnbonding) error {
	addrBz, err := k.addressCodec.StringToBytes(entry.Address)
	if err != nil {
		// The address was validated when unbonding began, so this only fires on
		// corrupt state. Failing loudly beats silently keeping someone's liquidity.
		return err
	}

	pool, err := k.Pool.Get(ctx, entry.PoolId)
	if err != nil {
		return err
	}
	// Compound pending rewards into the reserve before pricing against it: the
	// unbonding position held its shares the whole period, so it is owed its slice
	// of everything earned up to this moment.
	if err := k.settlePoolRewards(ctx, entry.PoolId, &pool); err != nil {
		return err
	}

	// Read supply before burning — the escrowed shares are still outstanding, and
	// they have to be in the denominator for the position to price at the fraction
	// of the pool it actually owns.
	total := k.totalShares(ctx, entry.PoolId).Amount
	if !total.IsPositive() || entry.Shares.Amount.GT(total) {
		return types.ErrInsufficientPool.Wrapf(
			"pool %d has %s shares outstanding against an unbonding of %s",
			entry.PoolId, total, entry.Shares.Amount)
	}

	outErth := sdk.NewCoin(pool.ReserveErth.Denom, entry.Shares.Amount.Mul(pool.ReserveErth.Amount).Quo(total))
	outToken := sdk.NewCoin(pool.ReserveToken.Denom, entry.Shares.Amount.Mul(pool.ReserveToken.Amount).Quo(total))

	if err := k.burnEscrowedShares(ctx, entry.Shares); err != nil {
		return err
	}

	pool.ReserveErth = pool.ReserveErth.Sub(outErth)
	pool.ReserveToken = pool.ReserveToken.Sub(outToken)
	if err := k.SetPool(ctx, entry.PoolId, pool); err != nil {
		return err
	}

	// A dust position can round both legs to zero. The shares are burned and the
	// entry cleared regardless, so it cannot sit in the queue being retried every
	// block forever.
	payout := sdk.NewCoins(outErth, outToken)
	if !payout.IsZero() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(addrBz), payout); err != nil {
			return err
		}
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent(
			"complete_unbond_liquidity",
			sdk.NewAttribute("pool_id", strconv.FormatUint(entry.PoolId, 10)),
			sdk.NewAttribute("provider", entry.Address),
			sdk.NewAttribute("shares", entry.Shares.String()),
			sdk.NewAttribute("amount_a", outErth.String()),
			sdk.NewAttribute("amount_b", outToken.String()),
		),
	)
	return nil
}
