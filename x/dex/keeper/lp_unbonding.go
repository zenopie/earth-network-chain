package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
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

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, m := range due {
		// Each payout gets its own branch, and the entry is dropped whether or not
		// it settles.
		//
		// This used to return the error, which took the chain down and kept it
		// down. The queue is ordered by completion time and the loop breaks at the
		// first entry not yet due, so a failing entry sits at the head; because it
		// is removed only after its payout succeeds, the next block reaches the
		// same entry and fails the same way. EndBlock errors are not recovered by
		// baseapp and every validator computes the same state, so that is a
		// permanent chain halt, from one malformed row, freezing every withdrawal
		// queued behind it. Only an upgrade could clear it. The doc comment above
		// claimed the remainder was never stranded; that is true of the sweep
		// limit it was written about and false when the oldest entry is the one
		// erroring.
		//
		// Dropping beats retrying: a retry changes nothing, and the escrowed
		// shares stay on the module account where CheckShareBacking reports them
		// rather than vanishing. Anything that did move coins wrongly is still
		// caught by AssertHotInvariants, which is deliberately left alone — its
		// halt is over a module already known to be wrong, which is a different
		// thing from this one.
		//
		// The cache branch is what makes the drop safe. payoutUnbonding settles
		// the pool, burns shares and writes reserves before it can fail, so
		// letting a half-finished payout persist would be its own corruption. On
		// failure the branch is discarded and only the removal below survives.
		//
		// A panic is dropped the same way (safeexec.Cached recovers it): a
		// cache branch does not unwind one, and a panic out of the EndBlocker
		// is the same permanent halt as the error was (audit 4: a math.Int
		// overflow in the payout).
		if err := safeexec.Cached(sdkCtx, func(cacheCtx sdk.Context) error {
			return k.payoutUnbonding(cacheCtx, m.entry)
		}); err != nil {
			sdkCtx.Logger().Error("lp unbonding payout failed — dropping the entry",
				"pool_id", m.entry.PoolId,
				"provider", m.entry.Address, // empty for a private withdrawal
				"shares", m.entry.Shares.String(),
				"height", sdkCtx.BlockHeight(),
				"err", err)
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				"lp_unbond_payout_failed",
				sdk.NewAttribute("pool_id", strconv.FormatUint(m.entry.PoolId, 10)),
				sdk.NewAttribute("provider", m.entry.Address),
				sdk.NewAttribute("shares", m.entry.Shares.String()),
				sdk.NewAttribute("error", err.Error()),
			))
		}
		if err := k.removeLpUnbonding(ctx, m.key); err != nil {
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
// burns the escrowed shares and sends the assets to the provider, or, for a
// private withdrawal (no address), mints both legs as notes.
func (k Keeper) payoutUnbonding(ctx context.Context, entry types.LpUnbonding) error {
	private := entry.Address == ""
	var addrBz []byte
	if private {
		if len(entry.WithdrawalId) == 0 || len(entry.ErthPc) == 0 || len(entry.Pc) == 0 {
			return types.ErrInvalidUnbonding.Wrapf("pool %d: a private withdrawal needs an id and both pcs", entry.PoolId)
		}
	} else {
		var err error
		if addrBz, err = k.addressCodec.StringToBytes(entry.Address); err != nil {
			// The address was validated when unbonding began, so this only fires on
			// corrupt state. Failing loudly beats silently keeping someone's liquidity.
			return err
		}
	}

	// Nil-checked before anything touches the arithmetic, because a nil math.Int
	// panics rather than erroring and a panic out of the EndBlocker kills the node
	// without saying why — strictly worse than the error path, and not something
	// the caller's cache branch can contain, since discarding writes does not
	// unwind a panic. Genesis validation now refuses these at import; this is the
	// second line, for state that predates it.
	//
	// The denom is checked for the same reason MsgRemoveLiquidity checks it
	// (msg_server_remove_liquidity.go): shares of the wrong pool would burn one
	// pool's supply while paying out of another's reserves, and the invariant
	// that caught it would name the wrong module.
	if entry.Shares.Amount.IsNil() || !entry.Shares.Amount.IsPositive() {
		return types.ErrInvalidUnbonding.Wrapf(
			"pool %d: unbonding for %s carries %s shares", entry.PoolId, entry.Address, entry.Shares.Amount)
	}
	if want := types.LPShareDenom(entry.PoolId); entry.Shares.Denom != want {
		return types.ErrInvalidUnbonding.Wrapf(
			"pool %d: unbonding is denominated in %s, not %s", entry.PoolId, entry.Shares.Denom, want)
	}

	pool, err := k.Pool.Get(ctx, entry.PoolId)
	if err != nil {
		return err
	}
	if pool.ReserveErth.Amount.IsNil() || pool.ReserveToken.Amount.IsNil() {
		return types.ErrInvalidUnbonding.Wrapf(
			"pool %d holds a nil reserve (%s / %s)", entry.PoolId, pool.ReserveErth, pool.ReserveToken)
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

	// big.Int: shares*reserve can pass math.Int's 256 bits on state that
	// predates the pool cap (types.MaxPoolAmount), and Mul panics there.
	erthAmt, err := mulDiv(entry.Shares.Amount, pool.ReserveErth.Amount, total)
	if err != nil {
		return types.ErrInvalidUnbonding.Wrapf("pool %d: %s", entry.PoolId, err)
	}
	tokenAmt, err := mulDiv(entry.Shares.Amount, pool.ReserveToken.Amount, total)
	if err != nil {
		return types.ErrInvalidUnbonding.Wrapf("pool %d: %s", entry.PoolId, err)
	}
	outErth := sdk.NewCoin(pool.ReserveErth.Denom, erthAmt)
	outToken := sdk.NewCoin(pool.ReserveToken.Denom, tokenAmt)

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
	//
	// A shielded-only token (the ANML of the ANML/ERTH pool) never reaches an
	// account: it is minted as a note to the pc the withdrawal named. The
	// ERTH leg goes to the account as for any pool.
	payout := sdk.NewCoins(outErth, outToken)
	if private {
		// A private withdrawal: both legs as notes, no account.
		for _, leg := range []struct {
			c      sdk.Coin
			pc, ct []byte
		}{{outErth, entry.ErthPc, entry.ErthCiphertext}, {outToken, entry.Pc, entry.Ciphertext}} {
			if leg.c.IsPositive() {
				if _, _, err := k.shielded.MintNote(ctx, types.ModuleName, leg.c, leg.pc, leg.ct); err != nil {
					return err
				}
			}
		}
		payout = nil
	} else if k.isShieldedOnly(outToken.Denom) {
		if outToken.IsPositive() {
			if len(entry.Pc) == 0 {
				return types.ErrInvalidUnbonding.Wrapf("pool %d: no pc to pay %s to", entry.PoolId, outToken)
			}
			if _, _, err := k.shielded.MintNote(ctx, types.ModuleName, outToken, entry.Pc, entry.Ciphertext); err != nil {
				return err
			}
		}
		payout = sdk.NewCoins(outErth)
	}
	if !payout.IsZero() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(addrBz), payout); err != nil {
			return err
		}
	}

	attrs := []sdk.Attribute{sdk.NewAttribute("pool_id", strconv.FormatUint(entry.PoolId, 10))}
	if !private {
		attrs = append(attrs, sdk.NewAttribute("provider", entry.Address))
	}
	attrs = append(attrs,
		sdk.NewAttribute("shares", entry.Shares.String()),
		sdk.NewAttribute("amount_a", outErth.String()),
		sdk.NewAttribute("amount_b", outToken.String()),
	)
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent("complete_unbond_liquidity", attrs...))
	return nil
}

// LpUnbondingKey is the store key of u: completion time, pool, and the
// provider's address bytes, or a private withdrawal's id.
func (k Keeper) LpUnbondingKey(u types.LpUnbonding) (collections.Triple[int64, uint64, []byte], error) {
	if u.Address == "" {
		return collections.Join3(u.CompletionTime, u.PoolId, u.WithdrawalId), nil
	}
	addrBz, err := k.addressCodec.StringToBytes(u.Address)
	if err != nil {
		return collections.Triple[int64, uint64, []byte]{}, err
	}
	return collections.Join3(u.CompletionTime, u.PoolId, addrBz), nil
}

// setLpUnbonding writes a withdrawal and its address index entry.
func (k Keeper) setLpUnbonding(ctx context.Context, key collections.Triple[int64, uint64, []byte], u types.LpUnbonding) error {
	if err := k.LpUnbondings.Set(ctx, key, u); err != nil {
		return err
	}
	return k.LpUnbondingsByAddr.Set(ctx, byAddrKey(key))
}

// removeLpUnbonding deletes a withdrawal and its address index entry.
func (k Keeper) removeLpUnbonding(ctx context.Context, key collections.Triple[int64, uint64, []byte]) error {
	if err := k.LpUnbondings.Remove(ctx, key); err != nil {
		return err
	}
	return k.LpUnbondingsByAddr.Remove(ctx, byAddrKey(key))
}

func byAddrKey(key collections.Triple[int64, uint64, []byte]) collections.Triple[[]byte, int64, uint64] {
	return collections.Join3(key.K3(), key.K1(), key.K2())
}

// IndexLpUnbondingsByAddr builds LpUnbondingsByAddr from LpUnbondings. For
// the v0.9.2 upgrade, which adds the index to a store that already holds
// withdrawals; idempotent.
func (k Keeper) IndexLpUnbondingsByAddr(ctx context.Context) error {
	return k.LpUnbondings.Walk(ctx, nil, func(key collections.Triple[int64, uint64, []byte], _ types.LpUnbonding) (bool, error) {
		return false, k.LpUnbondingsByAddr.Set(ctx, byAddrKey(key))
	})
}
