package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	"github.com/earth-network/earth/x/dex/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
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
	notes := 0
	for _, m := range due {
		if notes >= types.LpUnbondNoteBudget {
			// The rest are still due: the next block takes them, oldest first.
			capped = true
			break
		}
		// Each payout gets its own branch (safeexec.Cached, recovering
		// panics): payoutUnbonding settles the pool, burns shares and writes
		// reserves before it can fail, and a half-finished payout persisting
		// would be its own corruption. An EndBlock error or panic is a
		// permanent halt (every validator fails the same way).
		//
		// A failed payout is never dropped (audit 5 D1: dropping it lost the
		// escrowed shares for good, and the failure was forceable: swap into
		// the pool in the maturity block, pushing a private leg past what a
		// note can hold, swap back next block). The entry stays, with its
		// shares escrowed, and is retried later (retryUnbonding): moved off
		// the head of the queue, so it cannot stall the entries behind it,
		// and retried at a geometrically growing interval, so a permanently
		// failing entry costs a sweep slot ever more rarely and never halts.
		minted := 0
		if err := safeexec.Cached(sdkCtx, func(cacheCtx sdk.Context) error {
			n, err := k.payoutUnbonding(cacheCtx, m.entry, types.LpUnbondNoteBudget-notes)
			minted = n
			return err
		}); errors.Is(err, errNoteBudget) {
			// Its notes would take the sweep past its budget (audit 6
			// D-L-D1: the budget was checked only before each payout, which
			// can mint 2 x MaxSplitNotes): it waits for the next block, at
			// the head of the queue, not as a failure.
			capped = true
			break
		} else if err != nil {
			cause := err
			// Store writes only, but on its own branch too: a failure here
			// leaves the entry where it is (retried next block), never a halt.
			safeexec.Item(sdkCtx, types.ModuleName, "lp_unbond_retry", func(c sdk.Context) error {
				return k.retryUnbonding(c, m.key, m.entry, cause)
			})
			continue
		}
		notes += minted
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

// retryUnbonding re-files a matured entry whose payout failed: attempts+1,
// completion_time = now + LpUnbondRetryDelay(attempts), the shares still
// escrowed. A key taken at that time (the same provider's withdrawal maturing
// then) moves it a second later, up to a minute; past that it stays where it
// is and is retried next block.
func (k Keeper) retryUnbonding(ctx context.Context, key collections.Triple[int64, uint64, []byte], entry types.LpUnbonding, cause error) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	entry.PayoutAttempts++
	at := sdkCtx.BlockTime().Unix() + types.LpUnbondRetryDelay(entry.PayoutAttempts)
	moved := false
	for i := int64(0); i < 60; i++ {
		next := collections.Join3(at+i, key.K2(), key.K3())
		if has, err := k.LpUnbondings.Has(ctx, next); err != nil {
			return err
		} else if has {
			continue
		}
		if err := k.removeLpUnbonding(ctx, key); err != nil {
			return err
		}
		entry.CompletionTime = next.K1()
		if err := k.setLpUnbonding(ctx, next, entry); err != nil {
			return err
		}
		moved = true
		break
	}
	if !moved {
		if err := k.LpUnbondings.Set(ctx, key, entry); err != nil {
			return err
		}
	}
	sdkCtx.Logger().Error("lp unbonding payout failed; retrying later",
		"pool_id", entry.PoolId, "provider", entry.Address, "shares", entry.Shares.String(),
		"attempts", entry.PayoutAttempts, "retry_at", entry.CompletionTime, "err", cause)
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"lp_unbond_payout_failed",
		sdk.NewAttribute("pool_id", strconv.FormatUint(entry.PoolId, 10)),
		sdk.NewAttribute("provider", entry.Address),
		sdk.NewAttribute("shares", entry.Shares.String()),
		sdk.NewAttribute("attempts", strconv.FormatUint(uint64(entry.PayoutAttempts), 10)),
		sdk.NewAttribute("retry_at", strconv.FormatInt(entry.CompletionTime, 10)),
		sdk.NewAttribute("error", cause.Error()),
	))
	return nil
}

// errNoteBudget: the payout would mint more notes than the sweep has left.
var errNoteBudget = errors.New("lp unbond sweep: note budget spent")

// payoutUnbonding prices one matured entry against the pool as it stands now,
// burns the escrowed shares and sends the assets to the provider, or, for a
// private withdrawal (no address), mints both legs as notes. A note leg above
// a note's maximum (2^63-1) is paid as several notes (MintNoteSplit). Returns
// how many notes it minted. budget is how many notes the
// sweep has left: a payout needing more is refused with errNoteBudget before
// anything is written, unless the sweep has minted none yet (budget is the
// whole LpUnbondNoteBudget), so that one large payout always goes through.
func (k Keeper) payoutUnbonding(ctx context.Context, entry types.LpUnbonding, budget int) (int, error) {
	notes := 0
	private := entry.Address == ""
	var addrBz []byte
	if private {
		if len(entry.WithdrawalId) == 0 || len(entry.ErthPc) == 0 || len(entry.Pc) == 0 {
			return 0, types.ErrInvalidUnbonding.Wrapf("pool %d: a private withdrawal needs an id and both pcs", entry.PoolId)
		}
	} else {
		var err error
		if addrBz, err = k.addressCodec.StringToBytes(entry.Address); err != nil {
			// The address was validated when unbonding began, so this only fires on
			// corrupt state. Failing loudly beats silently keeping someone's liquidity.
			return 0, err
		}
	}

	// Nil-checked before anything touches the arithmetic, because a nil math.Int
	// panics rather than erroring and a panic out of the EndBlocker kills the node
	// without saying why — strictly worse than the error path, and not something
	// the caller's cache branch can contain, since discarding writes does not
	// unwind a panic. Genesis validation (ValidateGenesis) refuses these at
	// import; this is the second line.
	//
	// The denom is checked for the same reason MsgRemoveLiquidity checks it
	// (msg_server_remove_liquidity.go): shares of the wrong pool would burn one
	// pool's supply while paying out of another's reserves, and the invariant
	// that caught it would name the wrong module.
	if entry.Shares.Amount.IsNil() || !entry.Shares.Amount.IsPositive() {
		return 0, types.ErrInvalidUnbonding.Wrapf(
			"pool %d: unbonding for %s carries %s shares", entry.PoolId, entry.Address, entry.Shares.Amount)
	}
	if want := types.LPShareDenom(entry.PoolId); entry.Shares.Denom != want {
		return 0, types.ErrInvalidUnbonding.Wrapf(
			"pool %d: unbonding is denominated in %s, not %s", entry.PoolId, entry.Shares.Denom, want)
	}

	pool, err := k.Pool.Get(ctx, entry.PoolId)
	if err != nil {
		return 0, err
	}
	if pool.ReserveErth.Amount.IsNil() || pool.ReserveToken.Amount.IsNil() {
		return 0, types.ErrInvalidUnbonding.Wrapf(
			"pool %d holds a nil reserve (%s / %s)", entry.PoolId, pool.ReserveErth, pool.ReserveToken)
	}
	// Compound pending rewards into the reserve before pricing against it: the
	// unbonding position held its shares the whole period, so it is owed its slice
	// of everything earned up to this moment.
	if err := k.settlePoolRewards(ctx, entry.PoolId, &pool); err != nil {
		return 0, err
	}

	// Read supply before burning — the escrowed shares are still outstanding, and
	// they have to be in the denominator for the position to price at the fraction
	// of the pool it actually owns.
	total := k.totalShares(ctx, entry.PoolId).Amount
	if !total.IsPositive() || entry.Shares.Amount.GT(total) {
		return 0, types.ErrInsufficientPool.Wrapf(
			"pool %d has %s shares outstanding against an unbonding of %s",
			entry.PoolId, total, entry.Shares.Amount)
	}

	// big.Int: shares*reserve can pass math.Int's 256 bits on state that
	// predates the pool cap (types.MaxPoolAmount), and Mul panics there.
	erthAmt, err := mulDiv(entry.Shares.Amount, pool.ReserveErth.Amount, total)
	if err != nil {
		return 0, types.ErrInvalidUnbonding.Wrapf("pool %d: %s", entry.PoolId, err)
	}
	tokenAmt, err := mulDiv(entry.Shares.Amount, pool.ReserveToken.Amount, total)
	if err != nil {
		return 0, types.ErrInvalidUnbonding.Wrapf("pool %d: %s", entry.PoolId, err)
	}
	outErth := sdk.NewCoin(pool.ReserveErth.Denom, erthAmt)
	outToken := sdk.NewCoin(pool.ReserveToken.Denom, tokenAmt)

	// The notes this payout mints, counted before anything is written.
	var noteLegs []sdk.Coin
	if private {
		noteLegs = []sdk.Coin{outErth, outToken}
	} else if k.isShieldedOnly(outToken.Denom) {
		noteLegs = []sdk.Coin{outToken}
	}
	need := 0
	for _, c := range noteLegs {
		if c.IsPositive() {
			vs, err := shieldedtypes.SplitNoteValues(c.Amount)
			if err != nil {
				return 0, err
			}
			need += len(vs)
		}
	}
	if need > budget && budget < types.LpUnbondNoteBudget {
		return 0, errNoteBudget
	}

	if err := k.burnEscrowedShares(ctx, entry.Shares); err != nil {
		return 0, err
	}

	pool.ReserveErth = pool.ReserveErth.Sub(outErth)
	pool.ReserveToken = pool.ReserveToken.Sub(outToken)
	if err := k.SetPool(ctx, entry.PoolId, pool); err != nil {
		return 0, err
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
				ps, err := k.shielded.MintNoteSplit(ctx, types.ModuleName, leg.c, leg.pc, leg.ct)
				if err != nil {
					return 0, err
				}
				notes += len(ps)
			}
		}
		payout = nil
	} else if k.isShieldedOnly(outToken.Denom) {
		if outToken.IsPositive() {
			if len(entry.Pc) == 0 {
				return 0, types.ErrInvalidUnbonding.Wrapf("pool %d: no pc to pay %s to", entry.PoolId, outToken)
			}
			ps, err := k.shielded.MintNoteSplit(ctx, types.ModuleName, outToken, entry.Pc, entry.Ciphertext)
			if err != nil {
				return 0, err
			}
			notes += len(ps)
		}
		payout = sdk.NewCoins(outErth)
	}
	if !payout.IsZero() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(addrBz), payout); err != nil {
			return 0, err
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
	return notes, nil
}

// maxWithdrawalNoteLeg is the largest note leg a withdrawal may be worth when
// it starts: a quarter of what MintNoteSplit pays at maturity, so the pool
// can move 4x against the provider over the unbonding period (or be pushed
// there in the maturity block) before the payout fails and is retried.
var maxWithdrawalNoteLeg = math.NewIntFromUint64(shieldedtypes.MaxNoteValue).MulRaw(shieldedtypes.MaxSplitNotes / 4)

// checkWithdrawalNoteLegs refuses a withdrawal of shares from pool whose
// note legs (erth when erthNote, the token when tokenNote) are worth more
// than maxWithdrawalNoteLeg at the pool as it stands: split it into smaller
// withdrawals. total is the share supply the shares are part of.
func (k Keeper) checkWithdrawalNoteLegs(pool types.Pool, shares, total math.Int, erthNote, tokenNote bool) error {
	if !total.IsPositive() {
		return nil
	}
	for _, leg := range []struct {
		on      bool
		reserve sdk.Coin
	}{{erthNote, pool.ReserveErth}, {tokenNote, pool.ReserveToken}} {
		if !leg.on || leg.reserve.Amount.IsNil() {
			continue
		}
		v, err := mulDiv(shares, leg.reserve.Amount, total)
		if err != nil {
			return types.ErrInvalidAmount.Wrap(err.Error())
		}
		if v.GT(maxWithdrawalNoteLeg) {
			return errorsmod.Wrapf(types.ErrInvalidAmount,
				"the %s leg (%s) is above %s, the most one withdrawal pays as notes; withdraw in smaller parts",
				leg.reserve.Denom, v, maxWithdrawalNoteLeg)
		}
	}
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
