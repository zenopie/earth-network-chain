package keeper

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/internal/safeexec"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Undelegation payouts.
//
// MsgUndelegate books the derth's live ERTH value u into its epoch's record
// (validator, epoch) and queues an UnbondPayout {u, pc, ciphertext}. Nothing
// is minted then. Once the record is MATURED (its SDK unbonding entry paid
// the module, or the epoch end settled it from the queue), the chain mints
// u x payout / requested ERTH to pc as ordinary pool notes, by itself, in a
// later block's EndBlocker (a slash of the entry, or of the pending record,
// reaches every payout pro rata; the floor division's dust goes to the
// community pool when the record's last payout is made), with no fee and no
// tx from the owner.
//
// The sweep runs before matureRecords in the EndBlocker: a record matured
// by matureRecords is paid by x/staking's EndBlocker after this module's, so
// its payouts wait for the next block, when the ERTH is in the module.
//
// Bounded per block: at most UnbondPayoutSweepLimit payouts tried and
// UnbondPayoutNoteBudget notes minted (a payout above 2^63-1 is several
// notes, MintNoteSplit). A payout whose notes would pass the budget waits
// for the next block, unless it is the block's first. A payout that fails
// is never dropped: it moves to the retry queue at now + 1h << min(attempts
// - 1, 8), so it cannot hold up the payouts behind it, and is retried there
// (shieldedstaking_unbond_payout_failed). Due retries go first.

// errPayoutBudget: the payout would mint more notes than the sweep has left.
var errPayoutBudget = errors.New("unbond payout sweep: note budget spent")

// queuePayout records a payout for record (validator, epoch) and returns its
// id. The record must already hold value in requested and outstanding.
func (k Keeper) queuePayout(ctx context.Context, validator string, epoch uint64, value math.Int, pc, ciphertext []byte) (uint64, error) {
	id, err := k.UnbondPayoutSeq.Next(ctx)
	if err != nil {
		return 0, err
	}
	p := types.UnbondPayout{Id: id, Validator: validator, Epoch: epoch, Value: value, Pc: pc, Ciphertext: ciphertext}
	if err := k.UnbondPayouts.Set(ctx, id, p); err != nil {
		return 0, err
	}
	return id, k.PayoutsByRecord.Set(ctx, collections.Join3(validator, epoch, id))
}

// markMatured queues a record's payouts for the sweep, when it has any.
func (k Keeper) markMatured(ctx context.Context, r types.UnbondRecord) error {
	if !r.Outstanding.IsPositive() {
		return nil
	}
	return k.MaturedRecords.Set(ctx, collections.Join(r.Validator, r.Epoch))
}

// sweepPayouts pays what is due: retries whose time has come, then the
// untried payouts of MATURED records, oldest record first. Never fails.
func (k Keeper) sweepPayouts(ctx context.Context) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime().Unix()

	type due struct {
		id    uint64
		retry *collections.Pair[int64, uint64]
		first *collections.Triple[string, uint64, uint64]
	}
	var batch []due
	full := func() bool { return len(batch) >= types.UnbondPayoutSweepLimit }
	if err := k.guarded(ctx, func(cc context.Context) error {
		if err := k.PayoutRetries.Walk(cc, nil, func(key collections.Pair[int64, uint64]) (bool, error) {
			if key.K1() > now || full() {
				return true, nil
			}
			batch = append(batch, due{id: key.K2(), retry: &key})
			return false, nil
		}); err != nil {
			return err
		}
		var done []collections.Pair[string, uint64]
		err := k.MaturedRecords.Walk(cc, nil, func(rec collections.Pair[string, uint64]) (bool, error) {
			if full() {
				return true, nil
			}
			n := 0
			err := k.PayoutsByRecord.Walk(cc, collections.NewSuperPrefixedTripleRange[string, uint64, uint64](rec.K1(), rec.K2()),
				func(key collections.Triple[string, uint64, uint64]) (bool, error) {
					if full() {
						return true, nil
					}
					batch = append(batch, due{id: key.K3(), first: &key})
					n++
					return false, nil
				})
			if err != nil {
				return true, err
			}
			if n == 0 {
				// Every payout tried: what is left (if anything) is in the
				// retry queue, which keeps the record alive by itself.
				done = append(done, rec)
			}
			return false, nil
		})
		if err != nil {
			return err
		}
		for _, rec := range done {
			if err := k.MaturedRecords.Remove(cc, rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		k.failure(ctx, "unbond_payout_walk", "", err)
		return
	}

	notes, capped := 0, false
	for _, d := range batch {
		if notes >= types.UnbondPayoutNoteBudget {
			capped = true
			break
		}
		minted := 0
		err := safeexec.Cached(sdkCtx, func(cc sdk.Context) error {
			n, err := k.payout(cc, d.id, types.UnbondPayoutNoteBudget-notes)
			if err != nil {
				return err
			}
			minted = n
			if d.retry != nil {
				return k.PayoutRetries.Remove(cc, *d.retry)
			}
			return k.PayoutsByRecord.Remove(cc, *d.first)
		})
		if errors.Is(err, errPayoutBudget) {
			capped = true
			break
		}
		if err != nil {
			cause := err
			safeexec.Item(sdkCtx, types.ModuleName, "unbond_payout_retry", func(cc sdk.Context) error {
				return k.retryPayout(cc, d.id, d.retry, d.first, cause)
			})
			continue
		}
		notes += minted
	}
	if capped {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent("shieldedstaking_unbond_payout_sweep_capped",
			sdk.NewAttribute("limit", strconv.Itoa(types.UnbondPayoutSweepLimit)),
			sdk.NewAttribute("note_budget", strconv.Itoa(types.UnbondPayoutNoteBudget)),
		))
	}
}

// payout mints one payout and settles its record. budget is how many notes
// the sweep has left: a payout needing more is refused with errPayoutBudget
// before anything is written, unless the sweep has minted none yet. Returns
// how many notes it minted. The caller removes the queue key.
func (k Keeper) payout(ctx sdk.Context, id uint64, budget int) (int, error) {
	p, err := k.UnbondPayouts.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	key := collections.Join(p.Validator, p.Epoch)
	r, err := k.UnbondRecords.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if r.Status != types.UNBOND_STATUS_MATURED {
		return 0, types.ErrNotMatured.Wrapf("%s/%d is %s", p.Validator, p.Epoch, r.Status)
	}
	if p.Value.IsNil() || !p.Value.IsPositive() || p.Value.GT(r.Outstanding) || !r.Requested.IsPositive() {
		return 0, types.ErrInvariant.Wrapf("payout %d: value %s against outstanding %s", id, p.Value, r.Outstanding)
	}
	pay := p.Value.Mul(r.Payout).Quo(r.Requested)
	var positions []uint64
	if pay.IsPositive() {
		values, err := shieldedtypes.SplitNoteValues(pay)
		if err != nil {
			return 0, err
		}
		if len(values) > budget && budget < types.UnbondPayoutNoteBudget {
			return 0, errPayoutBudget
		}
		if positions, err = k.shielded.MintNoteSplit(ctx, types.ModuleName, sdk.NewCoin(types.BondDenom, pay), p.Pc, p.Ciphertext); err != nil {
			return 0, err
		}
	}
	r.Outstanding = r.Outstanding.Sub(p.Value)
	r.Paid = r.Paid.Add(pay)
	if r.Outstanding.IsZero() {
		// The record's last payout: the floor division's dust goes to the
		// community pool, and the record is done.
		if dust := r.Payout.Sub(r.Paid); dust.IsPositive() {
			if err := k.distr.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(types.BondDenom, dust)), k.modAddr); err != nil {
				return 0, err
			}
		}
		if err := k.UnbondRecords.Remove(ctx, key); err != nil {
			return 0, err
		}
		if err := k.MaturedRecords.Remove(ctx, key); err != nil {
			return 0, err
		}
	} else if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
		return 0, err
	}
	if err := k.UnbondPayouts.Remove(ctx, id); err != nil {
		return 0, err
	}
	ps := make([]string, len(positions))
	for i, pos := range positions {
		ps[i] = strconv.FormatUint(pos, 10)
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUnbondPayout,
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(id, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, p.Validator),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(p.Epoch, 10)),
		sdk.NewAttribute(types.AttributeKeyValue, p.Value.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, pay.String()),
		sdk.NewAttribute(types.AttributeKeyNotes, strconv.Itoa(len(positions))),
		sdk.NewAttribute(types.AttributeKeyPositions, strings.Join(ps, ",")),
	))
	return len(positions), nil
}

// retryPayout re-files a payout whose minting failed: attempts+1, retry_at =
// now + UnbondPayoutRetryDelay(attempts), keyed (retry_at, id) in the retry
// queue and off its old key.
func (k Keeper) retryPayout(ctx sdk.Context, id uint64, retry *collections.Pair[int64, uint64],
	first *collections.Triple[string, uint64, uint64], cause error,
) error {
	p, err := k.UnbondPayouts.Get(ctx, id)
	if err != nil {
		return err
	}
	if retry != nil {
		if err := k.PayoutRetries.Remove(ctx, *retry); err != nil {
			return err
		}
	}
	if first != nil {
		if err := k.PayoutsByRecord.Remove(ctx, *first); err != nil {
			return err
		}
	}
	p.PayoutAttempts++
	p.RetryAt = ctx.BlockTime().Unix() + types.UnbondPayoutRetryDelay(p.PayoutAttempts)
	if err := k.UnbondPayouts.Set(ctx, id, p); err != nil {
		return err
	}
	if err := k.PayoutRetries.Set(ctx, collections.Join(p.RetryAt, id)); err != nil {
		return err
	}
	k.logger(ctx).Error("unbond payout failed; retrying later", "id", id, "validator", p.Validator,
		"epoch", p.Epoch, "attempts", p.PayoutAttempts, "retry_at", p.RetryAt, "err", cause)
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUnbondPayoutFailed,
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(id, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, p.Validator),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(p.Epoch, 10)),
		sdk.NewAttribute(types.AttributeKeyAttempts, strconv.FormatUint(uint64(p.PayoutAttempts), 10)),
		sdk.NewAttribute(types.AttributeKeyRetryAt, strconv.FormatInt(p.RetryAt, 10)),
		sdk.NewAttribute(types.AttributeKeyError, cause.Error()),
	))
	return nil
}
