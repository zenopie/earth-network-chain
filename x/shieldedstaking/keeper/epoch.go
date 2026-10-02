package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
//     notes; compound every active validator operator's self-bond rewards;
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
	}
	k.sweepSnapshots(ctx)
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

// endEpoch executes the queues and starts the next epoch. The epoch advances
// whatever happened to individual validators: what failed stays queued.
func (k Keeper) endEpoch(ctx context.Context, epoch types.Epoch) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	var vals []string
	_ = k.Validators.Walk(ctx, nil, func(v string, _ types.ValidatorState) (bool, error) {
		vals = append(vals, v)
		return len(vals) >= types.EpochValidatorLimit, nil
	})
	for _, v := range vals {
		if err := k.guarded(ctx, func(cc context.Context) error { return k.processValidator(cc, v) }); err != nil {
			k.failure(ctx, "validator", v, err)
		}
	}
	k.compoundSelfBonds(ctx)
	k.reweighPositions(ctx)
	if err := k.guarded(ctx, k.sweepForeignRewards); err != nil {
		k.failure(ctx, "sweep", "", err)
	}
	if err := k.AssertInvariants(ctx); err != nil {
		k.logger(ctx).Error("private staking invariant broken", "err", err)
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeInvariant, sdk.NewAttribute(types.AttributeKeyError, err.Error())))
	}

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

// compoundSelfBonds re-delegates, for every active (bonded, unjailed)
// validator, its operator's self-bond rewards to the same validator from the
// operator account: a validator's self-bond auto-compounds as the module's
// delegations do. Only the delegation's own rewards, in uerth: commission is
// untouched and stays withdrawable. An operator whose withdraw address is
// another account is skipped (its rewards are paid there, as it asked). Each
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
	op := sdk.AccAddress(valAddr)
	if _, err := k.staking.GetDelegation(ctx, op, valAddr); err != nil {
		if errors.Is(err, stakingtypes.ErrNoDelegation) {
			return nil // no self-bond left (it was undelegated whole)
		}
		return err
	}
	if wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op); err != nil {
		return err
	} else if !wa.Equals(op) {
		return nil
	}
	before := k.bank.GetBalance(ctx, op, types.BondDenom).Amount
	if _, err := k.distr.WithdrawDelegationRewards(ctx, op, valAddr); err != nil {
		return err
	}
	// What the withdrawal paid the operator in uerth: its self-bond's
	// rewards, and nothing it held before.
	amt := k.bank.GetBalance(ctx, op, types.BondDenom).Amount.Sub(before)
	if !amt.IsPositive() {
		return nil
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
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSelfBond,
		sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		sdk.NewAttribute(types.AttributeKeyAmount, amt.String()),
	))
	return nil
}

// pendingRecords is v's PENDING records, in epoch order.
func (k Keeper) pendingRecords(ctx context.Context, valoper string) ([]types.UnbondRecord, error) {
	var out []types.UnbondRecord
	err := k.PendingRecords.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](valoper),
		func(key collections.Pair[string, uint64]) (bool, error) {
			r, err := k.UnbondRecords.Get(ctx, key)
			if err != nil {
				return true, err
			}
			out = append(out, r)
			return false, nil
		})
	return out, err
}

// processValidator is one validator's epoch end.
//
// Accounting is by balance deltas: x/distribution pays the module's rewards
// as a side effect of every delegation change (its
// BeforeDelegationSharesModified hook), not only on the explicit withdraw.
// Whatever arrives beyond the withdraw is v's reward too and joins v's queue
// for the next epoch.
func (k Keeper) processValidator(ctx context.Context, valoper string) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return err
	}
	val, err := k.valAddr(valoper)
	if err != nil {
		return err
	}
	balance := func() math.Int { return k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount }

	bal0 := balance()
	_, del, v, found, err := k.delegation(ctx, val)
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
	_ = v

	// Undelegate this epoch's unbond notes (and any a failed epoch left).
	records, err := k.pendingRecords(ctx, valoper)
	if err != nil {
		return err
	}
	target := math.ZeroInt()
	for _, r := range records {
		target = target.Add(r.Target)
	}
	fromQueue := math.ZeroInt()
	if target.IsPositive() {
		tokens, _, _, found, err := k.delegation(ctx, val)
		if err != nil {
			return err
		}
		if found && tokens.IsPositive() {
			amt := math.MinInt(target, tokens)
			shares, err := k.staking.ValidateUnbondAmount(ctx, k.modAddr, val, amt)
			if err != nil {
				return err
			}
			completion, returned, err := k.staking.Undelegate(ctx, k.modAddr, val, shares)
			if err != nil {
				return err
			}
			if err := k.startUnbonding(ctx, records, target, returned, sdkCtx.BlockHeight(), completion); err != nil {
				return err
			}
		} else {
			// Nothing bonded to undelegate from (the validator is gone or
			// slashed to nothing): pay what the queue holds, now.
			fromQueue = math.MinInt(target, queue.Sub(delegated))
			if err := k.matureFromQueue(ctx, records, target, fromQueue); err != nil {
				return err
			}
		}
	}

	bal2 := balance()
	late := bal2.Sub(bal1.Sub(delegated)) // rewards paid by the delegation changes
	vs.PendingDelegation = queue.Sub(delegated).Add(late).Sub(fromQueue)
	vs.PendingUndelegation = math.ZeroInt()
	b, s, err := k.Backing(ctx, valoper)
	if err != nil {
		return err
	}
	vs.EpochRate = rateOf(b, s)
	if err := k.Validators.Set(ctx, valoper, vs); err != nil {
		return err
	}
	if !s.IsPositive() && vs.PendingDelegation.IsZero() && !b.IsPositive() {
		// Nothing left to account for at v.
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
