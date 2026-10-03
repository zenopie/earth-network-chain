package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// The EndBlocker runs after x/gov and before x/staking. It NEVER returns an
// error and never panics out: every piece of work runs in its own cache
// context behind a recover, and a failure is logged, emitted as an event and
// retried later. A halt here would stop the chain over one validator's
// bookkeeping.
//
//  0. slashed: validators slashed this block take their live (post-slash)
//     rate as their epoch rate, and their positions re-weigh at it;
//  1. mature: records whose SDK unbonding entry completes in this block read
//     the entry's balance now — x/staking's EndBlocker, next, pays and deletes
//     it.
//  2. epoch end, once end_time has passed: per validator withdraw rewards,
//     delegate the queue plus the rewards, undelegate the epoch's unbond
//     notes; compound every active validator's self-bond rewards and
//     commission into its self-bond;
//     re-weigh positions; sweep non-ERTH rewards to the community pool.
//  3. forget proposals whose voting has ended (x/gov has tallied them).
//
// First of all it records the stake tree's root, if the block moved it (a
// root is an anchor from the end of the block that made it).
func (k Keeper) EndBlocker(ctx context.Context) error {
	if err := k.guarded(ctx, k.recordStakeRoot); err != nil {
		k.failure(ctx, "stake_root", "", err)
	}
	k.reweighSlashed(ctx)
	k.matureRecords(ctx)
	epoch, err := k.Epoch.Get(ctx)
	if err != nil {
		k.failure(ctx, "epoch", "", err)
	} else if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() >= epoch.EndTime {
		k.endEpoch(ctx, epoch)
	} else if epoch.Number > 0 {
		k.continueSweep(ctx, epoch.Number-1)
	}
	k.sweepSnapshots(ctx)
	k.releaseRetiredEscrows(ctx)
	return nil
}

// guarded runs fn in a cache context, writing it only on success. A panic is
// an error.
func (k Keeper) guarded(ctx context.Context, fn func(cc context.Context) error) (err error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cc, write := sdkCtx.CacheContext()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	if err = fn(cc); err != nil {
		return err
	}
	write()
	return nil
}

func (k Keeper) failure(ctx context.Context, stage, validator string, err error) {
	k.logger(ctx).Error("private staking: deferred", "stage", stage, "validator", validator, "err", err)
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEpochFailure,
		sdk.NewAttribute(types.AttributeKeyStage, stage),
		sdk.NewAttribute(types.AttributeKeyValidator, validator),
		sdk.NewAttribute(types.AttributeKeyError, err.Error()),
	))
}

// endEpoch starts (or, if the last one has not finished, goes on with) the
// sweep over the validator books, and starts the next epoch. The epoch
// advances whatever happened to individual validators: what failed stays
// queued.
func (k Keeper) endEpoch(ctx context.Context, epoch types.Epoch) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sweep := k.sweepState(ctx)
	if !sweep.Active {
		sweep = types.EpochSweep{Active: true}
	}
	k.checkUnbondingFloor(ctx)
	k.resyncBooks(ctx, k.sweepBooks(ctx, sweep, epoch.Number))
	k.compoundSelfBonds(ctx)
	if err := k.guarded(ctx, k.sweepForeignRewards); err != nil {
		k.failure(ctx, "sweep", "", err)
	}
	k.retryEscrowReleases(ctx)
	k.reportInvariants(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		k.failure(ctx, "params", "", err)
		return
	}
	now := sdkCtx.BlockTime().Unix()
	next := types.Epoch{Number: epoch.Number + 1, StartTime: now, EndTime: now + int64(params.EpochSeconds)}
	if err := k.Epoch.Set(ctx, next); err != nil {
		k.failure(ctx, "epoch", "", err)
		return
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEpoch,
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(epoch.Number, 10)),
	))
}

// checkUnbondingFloor re-checks, at each epoch end, the floor genesis and
// MsgUpdateParams enforce (checkUnbondingEntries): x/staking's governance may
// have changed unbonding_time or max_entries since. A violation is reported,
// never fatal: processValidator defers an undelegation that would not fit.
func (k Keeper) checkUnbondingFloor(ctx context.Context) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return
	}
	if err := k.checkUnbondingEntries(ctx, params); err != nil {
		k.failure(ctx, "unbonding_floor", "", err)
	}
}

func (k Keeper) sweepState(ctx context.Context) types.EpochSweep {
	sweep, err := k.EpochSweep.Get(ctx)
	if err != nil {
		return types.EpochSweep{}
	}
	return sweep
}

// continueSweep goes on with an epoch-end sweep in a block after the epoch
// end.
func (k Keeper) continueSweep(ctx context.Context, maxEpoch uint64) {
	sweep := k.sweepState(ctx)
	if !sweep.Active {
		return
	}
	k.resyncBooks(ctx, k.sweepBooks(ctx, sweep, maxEpoch))
}

// resyncBooks re-files the Groundworks voter of each book the sweep just
// processed (at the epoch rate processValidator set), for those with
// positions. The voters re-weigh with the bounded sweep, EpochValidatorLimit
// a block, never in one walk over every validator at the epoch end: until
// its book's turn, a validator's voter keeps the previous epoch's rate.
func (k Keeper) resyncBooks(ctx context.Context, vals []string) {
	for _, v := range vals {
		if _, err := k.GwEpoch.Get(ctx, v); err == nil {
			k.resyncValidatorVoter(ctx, v)
		}
	}
}

// sweepBooks processes up to EpochValidatorLimit books after sweep's cursor,
// in key order, settling their unbond records of epochs up to maxEpoch, and
// records how far it got: the sweep ends when it reaches the last book.
// Books the cap leaves out are processed in the next blocks, so none waits
// on how its key sorts (audit F1). Returns the books processed.
func (k Keeper) sweepBooks(ctx context.Context, sweep types.EpochSweep, maxEpoch uint64) []string {
	var rng collections.Ranger[string]
	if sweep.Cursor != "" {
		rng = new(collections.Range[string]).StartExclusive(sweep.Cursor)
	}
	var vals []string
	if err := k.guarded(ctx, func(cc context.Context) error {
		return k.Validators.Walk(cc, rng, func(v string, _ types.ValidatorState) (bool, error) {
			vals = append(vals, v)
			return len(vals) >= types.EpochValidatorLimit, nil
		})
	}); err != nil {
		k.failure(ctx, "sweep_walk", sweep.Cursor, err)
		return nil
	}
	for _, v := range vals {
		if err := k.guarded(ctx, func(cc context.Context) error { return k.processValidator(cc, v, maxEpoch) }); err != nil {
			k.failure(ctx, "validator", v, err)
		}
	}
	if len(vals) < types.EpochValidatorLimit {
		sweep = types.EpochSweep{}
	} else {
		sweep.Cursor = vals[len(vals)-1]
	}
	if err := k.EpochSweep.Set(ctx, sweep); err != nil {
		k.failure(ctx, "sweep_cursor", "", err)
	}
	return vals
}

// reportInvariants runs AssertInvariants at the epoch end, bounded (skipped,
// with an event, past InvariantBookLimit books, unbond records, positions and
// validators (counted by their reward escrows, twice: invariant 5 walks
// both)) and
// guarded: it reports a broken invariant or a panic, it never halts EndBlock.
func (k Keeper) reportInvariants(ctx context.Context) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	n := 0
	_ = k.Validators.Walk(ctx, nil, func(string, types.ValidatorState) (bool, error) {
		n++
		return n > types.InvariantBookLimit, nil
	})
	if n <= types.InvariantBookLimit {
		_ = k.UnbondRecords.Walk(ctx, nil, func(collections.Pair[string, uint64], types.UnbondRecord) (bool, error) {
			n++
			return n > types.InvariantBookLimit, nil
		})
	}
	if n <= types.InvariantBookLimit {
		// Positions are uncapped: the walks over them (invariants 2 and 6)
		// count against the same bound.
		_ = k.Positions.Walk(ctx, nil, func(uint64, types.Position) (bool, error) {
			n++
			return n > types.InvariantBookLimit, nil
		})
	}
	if n <= types.InvariantBookLimit {
		// Invariant 5 walks every x/staking validator and every reward
		// escrow (one per validator; validators are permissionless): the
		// escrows count against the same bound, standing in for both.
		_ = k.RewardEscrows.Walk(ctx, nil, func(_, _ []byte) (bool, error) {
			n += 2
			return n > types.InvariantBookLimit, nil
		})
	}
	if n > types.InvariantBookLimit {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeInvariant,
			sdk.NewAttribute(types.AttributeKeyError, "skipped: too many books, unbond records and positions for one block")))
		return
	}
	err := func() (err error) {
		cc, _ := sdkCtx.CacheContext() // read only: never written
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return k.AssertInvariants(cc)
	}()
	if err != nil {
		k.logger(ctx).Error("private staking invariant broken", "err", err)
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeInvariant, sdk.NewAttribute(types.AttributeKeyError, err.Error())))
	}
}

// compoundSelfBonds re-delegates, for every active (bonded, unjailed)
// validator, its operator's self-bond rewards and its commission to the same
// validator: a validator's self-bond auto-compounds as the module's
// delegations do. Both are paid to the validator's reward escrow (the
// operator's withdraw address, escrow.go) — the explicit withdrawals here
// and whatever a self-bond change paid since the last epoch — and the
// escrow's uerth moves to the operator account and is self-delegated in the
// same cache context, so none of it is ever liquid. Only uerth is
// re-delegated (another denom stays in the escrow). Neither can be
// withdrawn by a msg (the ante and app's message router refuse
// MsgWithdrawDelegatorReward and MsgWithdrawValidatorCommission on every
// route), so the operator's only exit for them is unbonding the self-bond.
// A jailed validator is skipped: its commission accrues in x/distribution
// and its escrow keeps what it is paid, until it is active again (or
// removed: AfterValidatorRemoved releases the escrow). An operator whose
// withdraw address points anywhere but its escrow has it reset first. Each
// validator runs in its own cache context; a failure is logged, emitted and
// skipped, never returned. The re-delegation moves the operator's bond, so
// x/allocation's staking hook resyncs its Groundworks weight.
func (k Keeper) compoundSelfBonds(ctx context.Context) {
	vals, err := k.staking.GetBondedValidatorsByPower(ctx)
	if err != nil {
		k.failure(ctx, "self_bond", "", err)
		return
	}
	for _, val := range vals {
		if val.IsJailed() {
			continue
		}
		if err := k.guarded(ctx, func(cc context.Context) error { return k.compoundSelfBond(cc, val) }); err != nil {
			k.failure(ctx, "self_bond", val.GetOperator(), err)
		}
	}
}

func (k Keeper) compoundSelfBond(ctx context.Context, val stakingtypes.Validator) error {
	valAddr, err := k.staking.ValidatorAddressCodec().StringToBytes(val.GetOperator())
	if err != nil {
		return err
	}
	op, escrow := sdk.AccAddress(valAddr), types.RewardEscrowAddress(valAddr)
	if _, err := k.staking.GetDelegation(ctx, op, valAddr); err != nil {
		if errors.Is(err, stakingtypes.ErrNoDelegation) {
			return nil // no self-bond left (it was undelegated whole)
		}
		return err
	}
	// The rewards must land in the escrow: never skip an operator for a
	// withdraw address elsewhere, reset it (withdraw_addr.go).
	if err := k.setOperatorEscrow(ctx, valAddr, false); err != nil {
		return err
	}
	if _, err := k.distr.WithdrawDelegationRewards(ctx, op, valAddr); err != nil {
		return err
	}
	if _, err := k.distr.WithdrawValidatorCommission(ctx, valAddr); err != nil &&
		!errors.Is(err, distrtypes.ErrNoValidatorCommission) {
		return err
	}
	// Everything the escrow holds in uerth: this epoch's self-bond rewards
	// and commission, and what self-bond changes paid it since the last.
	amt := k.bank.GetBalance(ctx, escrow, types.BondDenom).Amount
	if !amt.IsPositive() {
		return nil
	}
	// Paid in and delegated straight back: the operator's spendable balance
	// must come out where it went in. It would not for a vesting operator
	// (refused at creation; see refuseVestingOperator), whose delegation x/bank
	// counts against vesting coins first. Checked, so nothing slips through
	// another way: compounding is undone (guarded) rather than unlock coins.
	spendable := k.bank.SpendableCoin(ctx, op, types.BondDenom).Amount
	if err := k.bank.SendCoins(ctx, escrow, op, sdk.NewCoins(sdk.NewCoin(types.BondDenom, amt))); err != nil {
		return err
	}
	// Re-read: the withdrawal touched the validator's distribution period,
	// not its tokens, but Delegate takes the validator by value.
	v, err := k.staking.GetValidator(ctx, valAddr)
	if err != nil {
		return err
	}
	if _, err := k.staking.Delegate(ctx, op, amt, stakingtypes.Unbonded, v, true); err != nil {
		return err
	}
	if after := k.bank.SpendableCoin(ctx, op, types.BondDenom).Amount; !after.Equal(spendable) {
		return errorsmod.Wrapf(types.ErrVestingOperator, "compounding moved operator %s's spendable %s from %s to %s",
			op, types.BondDenom, spendable, after)
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSelfBond,
		sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		sdk.NewAttribute(types.AttributeKeyAmount, amt.String()),
	))
	return nil
}

// unbondingEntriesFull reports whether the module's unbonding delegation to
// val already holds x/staking's max_entries entries, so another undelegation
// would be refused.
func (k Keeper) unbondingEntriesFull(ctx context.Context, val sdk.ValAddress) (bool, error) {
	maxEntries, err := k.staking.MaxEntries(ctx)
	if err != nil {
		return false, err
	}
	ubd, err := k.staking.GetUnbondingDelegation(ctx, k.modAddr, val)
	if errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return uint32(len(ubd.Entries)) >= maxEntries, nil
}

// pendingRecords is v's PENDING records, in epoch order.
func (k Keeper) pendingRecords(ctx context.Context, valoper string) ([]types.UnbondRecord, error) {
	return k.pendingRecordsUpTo(ctx, valoper, ^uint64(0))
}

// pendingRecordsUpTo is v's PENDING records of epochs up to maxEpoch.
func (k Keeper) pendingRecordsUpTo(ctx context.Context, valoper string, maxEpoch uint64) ([]types.UnbondRecord, error) {
	var out []types.UnbondRecord
	err := k.PendingRecords.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](valoper),
		func(key collections.Pair[string, uint64]) (bool, error) {
			if key.K2() > maxEpoch {
				return true, nil
			}
			r, err := k.UnbondRecords.Get(ctx, key)
			if err != nil {
				return true, err
			}
			out = append(out, r)
			return false, nil
		})
	return out, err
}

// processValidator is one validator's epoch end: its unbond records of
// epochs up to maxEpoch (the epoch that ended; a record of the epoch under
// way stays PENDING, Undelegate is still adding to it).
//
// Accounting is by balance deltas: x/distribution pays the module's rewards
// as a side effect of every delegation change (its
// BeforeDelegationSharesModified hook), not only on the explicit withdraw.
// Whatever arrives beyond the withdraw is v's reward too and joins v's queue
// for the next epoch.
//
// A book left with no derth (S_v == 0) and no later records owns nothing it
// backs: the module's whole delegation goes out with its last records (the
// rewards accrued since their notes were minted are theirs: their stake
// earned them), or, with no records at all, into an orphan record whose
// payout goes to the community pool; and what is queued is sent to the
// community pool. So the next delegator to v never buys backing nobody owns
// (audit F5), and the book empties and is removed.
func (k Keeper) processValidator(ctx context.Context, valoper string, maxEpoch uint64) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return err
	}
	val, err := k.valAddr(valoper)
	if err != nil {
		return err
	}
	if err := k.sweepOrphanRecords(ctx, valoper); err != nil {
		return err
	}
	balance := func() math.Int { return k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount }

	bal0 := balance()
	_, del, _, found, err := k.delegation(ctx, val)
	if err != nil {
		return err
	}
	if found && del.Shares.IsPositive() {
		if _, err := k.distr.WithdrawDelegationRewards(ctx, k.modAddr, val); err != nil {
			return err
		}
	}
	bal1 := balance()
	rewards := bal1.Sub(bal0)
	queue := vs.PendingDelegation.Add(rewards)

	// Delegate the queue, unless the validator cannot take it: gone, or
	// slashed to nothing. Jailed and tombstoned validators still take the
	// queue: the msgs that filled it were checked when they were sent, and the
	// derth they minted is backed by it either way.
	delegated := math.ZeroInt()
	if v2, err := k.staking.GetValidator(ctx, val); err == nil && queue.IsPositive() &&
		v2.Tokens.IsPositive() && !v2.InvalidExRate() {
		if _, err := k.staking.Delegate(ctx, k.modAddr, queue, stakingtypes.Unbonded, v2, true); err != nil {
			return err
		}
		delegated = queue
	} else if err != nil && !errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return err
	}

	// Undelegate the ended epochs' unbond notes (and any a failed epoch left).
	records, err := k.pendingRecordsUpTo(ctx, valoper, maxEpoch)
	if err != nil {
		return err
	}
	all, err := k.pendingRecords(ctx, valoper)
	if err != nil {
		return err
	}
	// settling: nobody holds derth/v and every pending record is in this
	// batch, so nothing left at v is owed to anyone but these records.
	settling := !vs.DerthSupply.IsPositive() && len(all) == len(records)
	target := math.ZeroInt()
	for _, r := range records {
		target = target.Add(r.Target)
	}
	fromQueue := math.ZeroInt()
	// x/staking refuses an undelegation past max_entries entries for one
	// (delegator, validator); governance can lower max_entries or raise
	// unbonding_time after the floor was checked (checkUnbondingEntries).
	// Rather than fail the whole book every block, the undelegation waits:
	// its records stay PENDING for the next epoch end, everything else here
	// goes ahead.
	if len(records) > 0 || settling {
		full, err := k.unbondingEntriesFull(ctx, val)
		if err != nil {
			return err
		}
		if full {
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUnbondingDeferred,
				sdk.NewAttribute(types.AttributeKeyValidator, valoper),
				sdk.NewAttribute(types.AttributeKeyAmount, target.String()),
			))
			records, target, settling = nil, math.ZeroInt(), false
		}
	}
	if len(records) > 0 || settling {
		tokens, _, _, found, err := k.delegation(ctx, val)
		if err != nil {
			return err
		}
		switch {
		case found && tokens.IsPositive() && (settling || target.IsPositive()):
			var shares math.LegacyDec
			if settling {
				d, err := k.staking.GetDelegation(ctx, k.modAddr, val)
				if err != nil {
					return err
				}
				shares = d.Shares
			} else if shares, err = k.staking.ValidateUnbondAmount(ctx, k.modAddr, val, math.MinInt(target, tokens)); err != nil {
				return err
			}
			if len(records) == 0 {
				if err := k.orphanUnbonding(ctx, valoper, maxEpoch, val, shares); err != nil {
					return err
				}
				break
			}
			completion, returned, err := k.staking.Undelegate(ctx, k.modAddr, val, shares)
			if err != nil {
				return err
			}
			if err := k.startUnbonding(ctx, records, target, returned, sdkCtx.BlockHeight(), completion); err != nil {
				return err
			}
		case len(records) > 0:
			// Nothing bonded to undelegate from (the validator is gone or
			// slashed to nothing), or nothing to undelegate (a slash took
			// the records' whole target): pay what the queue holds, now.
			fromQueue = math.MinInt(target, queue.Sub(delegated))
			if err := k.matureFromQueue(ctx, records, target, fromQueue); err != nil {
				return err
			}
		}
	}

	bal2 := balance()
	late := bal2.Sub(bal1.Sub(delegated)) // rewards paid by the delegation changes
	vs.PendingDelegation = queue.Sub(delegated).Add(late).Sub(fromQueue)
	vs.PendingUndelegation = vs.PendingUndelegation.Sub(target)
	if vs.PendingUndelegation.IsNegative() {
		vs.PendingUndelegation = math.ZeroInt()
	}
	if settling && vs.PendingDelegation.IsPositive() {
		// Owned by nobody: no derth, no record left to pay.
		if err := k.distr.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(types.BondDenom, vs.PendingDelegation)), k.modAddr); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeOrphan,
			sdk.NewAttribute(types.AttributeKeyValidator, valoper),
			sdk.NewAttribute(types.AttributeKeyAmount, vs.PendingDelegation.String()),
		))
		vs.PendingDelegation = math.ZeroInt()
	}
	b, s, err := k.Backing(ctx, valoper)
	if err != nil {
		return err
	}
	vs.EpochRate = rateOf(b, s)
	if err := k.Validators.Set(ctx, valoper, vs); err != nil {
		return err
	}
	orphans, err := k.hasOrphanRecords(ctx, valoper)
	if err != nil {
		return err
	}
	if !s.IsPositive() && vs.PendingDelegation.IsZero() && vs.PendingUndelegation.IsZero() && !b.IsPositive() &&
		len(all) == len(records) && !orphans {
		// Nothing left to account for at v (an orphan record keeps the book
		// until its payout is swept).
		if err := k.Validators.Remove(ctx, valoper); err != nil {
			return err
		}
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEpochValidator,
		sdk.NewAttribute(types.AttributeKeyValidator, valoper),
		sdk.NewAttribute(types.AttributeKeyRewards, rewards.Add(late).String()),
		sdk.NewAttribute(types.AttributeKeyDelegated, delegated.String()),
		sdk.NewAttribute(types.AttributeKeyUndelegated, target.String()),
		sdk.NewAttribute(types.AttributeKeyRate, vs.EpochRate.String()),
		sdk.NewAttribute(types.AttributeKeySupply, s.String()),
	))
	return nil
}

// orphanUnbonding undelegates shares that back no derth and no record (a
// book left with a delegation and no derth, as books could be before audit
// F5's fix) into an orphan record: requested and outstanding zero, so no
// note can claim it; sweepOrphanRecords sends its payout to the community
// pool once it matures. Skipped (left for a later epoch) if v already has a
// record for maxEpoch.
func (k Keeper) orphanUnbonding(ctx context.Context, valoper string, maxEpoch uint64, val sdk.ValAddress, shares math.LegacyDec) error {
	key := collections.Join(valoper, maxEpoch)
	if ok, err := k.UnbondRecords.Has(ctx, key); err != nil || ok {
		return err
	}
	completion, returned, err := k.staking.Undelegate(ctx, k.modAddr, val, shares)
	if err != nil {
		return err
	}
	r := types.UnbondRecord{
		Validator: valoper, Epoch: maxEpoch, Status: types.UNBOND_STATUS_UNBONDING,
		Requested: math.ZeroInt(), Target: math.ZeroInt(), Undelegated: returned,
		Payout: math.ZeroInt(), Outstanding: math.ZeroInt(), Paid: math.ZeroInt(),
		CreationHeight: sdk.UnwrapSDKContext(ctx).BlockHeight(), CompletionTime: completion.UnixNano(),
	}
	if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
		return err
	}
	return k.MaturityQueue.Set(ctx, collections.Join3(r.CompletionTime, valoper, maxEpoch))
}

func (k Keeper) hasOrphanRecords(ctx context.Context, valoper string) (bool, error) {
	found := false
	err := k.UnbondRecords.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](valoper),
		func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
			found = r.Requested.IsZero()
			return found, nil
		})
	return found, err
}

// sweepOrphanRecords sends the payout of v's matured orphan records (no note
// was ever minted against them) to the community pool, and forgets them.
func (k Keeper) sweepOrphanRecords(ctx context.Context, valoper string) error {
	var done []types.UnbondRecord
	if err := k.UnbondRecords.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](valoper),
		func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
			if r.Status == types.UNBOND_STATUS_MATURED && r.Requested.IsZero() {
				done = append(done, r)
			}
			return false, nil
		}); err != nil {
		return err
	}
	for _, r := range done {
		if amt := r.Payout.Sub(r.Paid); amt.IsPositive() {
			if err := k.distr.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(types.BondDenom, amt)), k.modAddr); err != nil {
				return err
			}
			sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeOrphan,
				sdk.NewAttribute(types.AttributeKeyValidator, valoper),
				sdk.NewAttribute(types.AttributeKeyAmount, amt.String()),
			))
		}
		if err := k.UnbondRecords.Remove(ctx, collections.Join(r.Validator, r.Epoch)); err != nil {
			return err
		}
	}
	return nil
}

// share splits total across records pro rata by weight; the last takes the
// remainder, so the parts sum to total exactly.
func share(total math.Int, weights []math.Int) []math.Int {
	sum := math.ZeroInt()
	for _, w := range weights {
		sum = sum.Add(w)
	}
	out := make([]math.Int, len(weights))
	left := total
	for i, w := range weights {
		if i == len(weights)-1 || !sum.IsPositive() {
			out[i] = left
			left = math.ZeroInt()
			continue
		}
		out[i] = total.Mul(w).Quo(sum)
		left = left.Sub(out[i])
	}
	return out
}

func targets(records []types.UnbondRecord) []math.Int {
	w := make([]math.Int, len(records))
	for i, r := range records {
		w[i] = r.Target
	}
	return w
}

// startUnbonding records one SDK undelegation against the records it serves.
func (k Keeper) startUnbonding(ctx context.Context, records []types.UnbondRecord, _ math.Int, returned math.Int, height int64, completion time.Time) error {
	parts := share(returned, targets(records))
	for i, r := range records {
		key := collections.Join(r.Validator, r.Epoch)
		r.Status = types.UNBOND_STATUS_UNBONDING
		r.Undelegated = parts[i]
		r.CreationHeight = height
		r.CompletionTime = completion.UnixNano()
		if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
			return err
		}
		if err := k.PendingRecords.Remove(ctx, key); err != nil {
			return err
		}
		if err := k.MaturityQueue.Set(ctx, collections.Join3(r.CompletionTime, r.Validator, r.Epoch)); err != nil {
			return err
		}
	}
	return nil
}

// matureFromQueue settles records with no bonded stake behind them out of the
// validator's queued ERTH.
func (k Keeper) matureFromQueue(ctx context.Context, records []types.UnbondRecord, _ math.Int, paid math.Int) error {
	parts := share(paid, targets(records))
	for i, r := range records {
		key := collections.Join(r.Validator, r.Epoch)
		r.Status = types.UNBOND_STATUS_MATURED
		r.Payout = parts[i]
		r.CreationHeight = sdk.UnwrapSDKContext(ctx).BlockHeight()
		if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
			return err
		}
		if err := k.PendingRecords.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// matureRecords reads the SDK entries completing in this block. It must run
// before x/staking's EndBlocker, which pays and deletes them: the module's
// balance then rises by exactly the sum of the payouts recorded here.
//
// Records sharing an entry (same validator and creation height: SDK entries
// merge) split its balance pro rata by what each undelegated.
func (k Keeper) matureRecords(ctx context.Context) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	type group struct {
		valoper string
		height  int64
		keys    []collections.Pair[string, uint64]
		queued  []collections.Triple[int64, string, uint64]
	}
	var groups []*group
	idx := map[string]*group{}
	_ = k.MaturityQueue.Walk(ctx, nil, func(key collections.Triple[int64, string, uint64]) (bool, error) {
		if key.K1() > now {
			return true, nil
		}
		r, err := k.UnbondRecords.Get(ctx, collections.Join(key.K2(), key.K3()))
		if err != nil {
			return false, nil
		}
		id := fmt.Sprintf("%s/%d", r.Validator, r.CreationHeight)
		g, ok := idx[id]
		if !ok {
			g = &group{valoper: r.Validator, height: r.CreationHeight}
			idx[id] = g
			groups = append(groups, g)
		}
		g.keys = append(g.keys, collections.Join(key.K2(), key.K3()))
		g.queued = append(g.queued, key)
		return false, nil
	})
	for _, g := range groups {
		err := k.guarded(ctx, func(cc context.Context) error {
			if err := k.matureGroup(cc, g.valoper, g.height, g.keys); err != nil {
				return err
			}
			for _, key := range g.queued {
				if err := k.MaturityQueue.Remove(cc, key); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			// Left queued. Only a store failure gets here; the retry reads the
			// entry after x/staking paid it and records a payout of zero, which
			// AssertInvariants then reports as a surplus.
			k.failure(ctx, "mature", g.valoper, err)
		}
	}
}

func (k Keeper) matureGroup(ctx context.Context, valoper string, height int64, keys []collections.Pair[string, uint64]) error {
	val, err := k.valAddr(valoper)
	if err != nil {
		return err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	balance := math.ZeroInt()
	if ubd, err := k.staking.GetUnbondingDelegation(ctx, k.modAddr, val); err == nil {
		for _, e := range ubd.Entries {
			if e.CreationHeight == height && e.IsMature(now) {
				balance = balance.Add(e.Balance)
			}
		}
	} else if !errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
		return err
	}
	records := make([]types.UnbondRecord, len(keys))
	weights := make([]math.Int, len(keys))
	for i, key := range keys {
		r, err := k.UnbondRecords.Get(ctx, key)
		if err != nil {
			return err
		}
		records[i], weights[i] = r, r.Undelegated
	}
	parts := share(balance, weights)
	for i, r := range records {
		r.Status = types.UNBOND_STATUS_MATURED
		r.Payout = parts[i]
		if err := k.UnbondRecords.Set(ctx, keys[i], r); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeMatured,
			sdk.NewAttribute(types.AttributeKeyDenom, types.UnbondDenom(r.Validator, r.Epoch)),
			sdk.NewAttribute(types.AttributeKeyValue, r.Requested.String()),
			sdk.NewAttribute(types.AttributeKeyPayout, r.Payout.String()),
		))
	}
	return nil
}

// sweepForeignRewards sends every denom the module holds that it has no use
// for (rewards paid in fee denoms other than uerth) to the community pool.
// Only uerth can be re-delegated.
func (k Keeper) sweepForeignRewards(ctx context.Context) error {
	var sweep sdk.Coins
	for _, c := range k.bank.GetAllBalances(ctx, k.modAddr) {
		if c.Denom == types.BondDenom || strings.HasPrefix(c.Denom, types.DerthPrefix) || strings.HasPrefix(c.Denom, types.UnbondPrefix) {
			continue
		}
		sweep = append(sweep, c)
	}
	if sweep.Empty() {
		return nil
	}
	return k.distr.FundCommunityPool(ctx, sweep, k.modAddr)
}
