package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// Dead options do not stay forever.
//
// Adding an ADDRESS option is permissionless: anyone may add one by burning the
// fee, and nothing ever removed it again. The fee makes a large number of them
// expensive rather than free, which is the real brake — but a paid-once row that
// the chain stores forever is still growth with no way back, and most of it will
// be options nobody ever voted for.
//
// So an option carrying no weight is scheduled for removal, and swept away once
// it has been in that state for OptionIdleGrace.  Coming back to life cancels
// the removal; dying again restarts the clock.
//
// Unclaimed rewards are forfeited with it. Thirty days without a single voter is
// long enough, and anyone may trigger the claim — an ADDRESS option with no
// claimer set can be claimed by a passer-by, and the payout goes to the
// recipient either way — so a live recipient has thirty days and a permissionless
// way to take what is theirs.
//
// A forfeited balance is burned. It used to be enough to simply not mint it —
// rewards were issued at the moment they were claimed, so an unclaimed balance
// was ERTH that never existed. Emission is now minted as it accrues, so the
// coins behind a dead option's balance are real and sitting in this module's
// account; leaving them there would put the module permanently out of balance
// with what its options say they hold. Burning reproduces the old economics
// exactly: supply ends up where it would have been had the option never been
// paid.
//
// The stream's own accounting is untouched either way — the invariant is over
// AmountAllocated, which is zero on anything prunable by definition.
//
// The structural fact that makes this safe: zero weight means no live voter is
// pointing at it. resyncVoter subtracts a voter's contribution option by option,
// so an option someone is allocating to has a positive balance by construction.
// Removing a zero-weight option therefore cannot leave a voter's split
// subtracting from something that is no longer there, and cannot move
// TotalWeight or SummedWeight — which is why the sweep does not go through
// setOption.
//
// Ids are never reused — OptionSeq only goes up — so a removed option's id
// cannot come back attached to something else, and a voter still naming it in an
// old split gets ErrOptionNotFound rather than a stranger's option.

// prunable reports whether an option is dead: permissionlessly added and
// carrying no weight.
//
// An unclaimed balance does not save it — see the note on forfeiture above.
func prunable(opt types.AllocationOption) bool {
	if opt.Kind != types.ALLOCATION_KIND_ADDRESS {
		// INTEGRATED options are governance's, resolved by a protocol handler
		// every block. An idle one is idle on purpose.
		return false
	}
	return opt.AmountAllocated.IsNil() || opt.AmountAllocated.IsZero()
}

// refreshPruneSchedule brings an option's removal schedule in line with what it
// now holds. Called on every option write, so the schedule cannot fall behind
// the record it describes — including the writes nobody sent a transaction for,
// since a lapsed registration and a stake that unbonds both reach an option
// through resyncVoter like everything else.
//
// An option that is already scheduled keeps its original due date rather than
// having it pushed out by each write. Otherwise a claim on a dead option would
// postpone its removal by another grace period, and repeating that would keep it
// alive forever without a single vote.
func (k Keeper) refreshPruneSchedule(ctx context.Context, stream types.StreamId, opt types.AllocationOption) error {
	kk := optionKey(stream, opt.Id)

	due, err := k.PruneDue.Get(ctx, kk)
	scheduled := err == nil
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	if prunable(opt) {
		if scheduled {
			return nil
		}
		at := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + types.OptionIdleGrace
		if err := k.PruneDue.Set(ctx, kk, at); err != nil {
			return err
		}
		return k.PruneQueue.Set(ctx, collections.Join3(at, key(stream), opt.Id))
	}

	if !scheduled {
		return nil
	}
	if err := k.PruneQueue.Remove(ctx, collections.Join3(due, key(stream), opt.Id)); err != nil {
		return err
	}
	return k.PruneDue.Remove(ctx, kk)
}

// SweepPrunableOptions removes the options whose grace period has expired.
//
// Bounded twice over: it stops at the first entry that is not due yet, because
// the queue is ordered by time, and it stops after OptionPruneSweepLimit
// removals whatever else is waiting. What it does not remove stays at the front
// of the queue for the next block.
func (k Keeper) SweepPrunableOptions(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()

	due := make([]collections.Triple[int64, uint32, uint64], 0, types.OptionPruneSweepLimit)

	iter, err := k.PruneQueue.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	capped := false
	for ; iter.Valid(); iter.Next() {
		entry, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		if entry.K1() > now {
			break // ordered by due time: nothing after this is due either
		}
		if len(due) == types.OptionPruneSweepLimit {
			capped = true
			break
		}
		due = append(due, entry)
	}
	iter.Close()

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, entry := range due {
		stream, id := types.StreamId(entry.K2()), entry.K3()
		kk := optionKey(stream, id)

		if err := k.PruneQueue.Remove(ctx, entry); err != nil {
			return err
		}
		if err := k.PruneDue.Remove(ctx, kk); err != nil {
			return err
		}

		opt, err := k.Options.Get(ctx, kk)
		if errors.Is(err, collections.ErrNotFound) {
			continue // already gone; the schedule entry was the last of it
		} else if err != nil {
			return err
		}
		// Re-checked rather than trusted. The schedule is maintained on every
		// write, so a scheduled option that is no longer dead would be a bug —
		// and one that removed a live option would be unrecoverable.
		if !prunable(opt) {
			continue
		}
		if err := k.Options.Remove(ctx, kk); err != nil {
			return err
		}

		// forfeited is ERTH the option had earned and nobody claimed. The coins
		// exist — AdvanceIndex minted them as they accrued — so they are burned
		// rather than merely reported, which is what keeps the module's balance
		// equal to what its live options are owed.
		forfeited := math.ZeroInt()
		if !opt.Accumulated.IsNil() {
			forfeited = opt.Accumulated
		}
		// Both halves of the ledger, or neither. The removal above bypasses
		// setOption — deliberately, see the note at the top of this file, because
		// a zero-weight option cannot move TotalWeight or SummedWeight — but
		// SummedAccrued is a second running sum that setOption also maintains,
		// and it is over Accumulated rather than AmountAllocated. A prunable
		// option has zero weight by definition; it does NOT have a zero balance,
		// which is the entire reason the burn below exists.
		//
		// Left out, the burn drops Held by forfeited while SummedAccrued keeps
		// counting it, CheckSolvency reads Short, and AssertHotInvariants halts
		// the chain in this very block — the sweep runs in BeginBlock and the
		// assertion in EndBlock. The burn was added to protect solvency and
		// would have been the thing that broke it.
		if forfeited.IsPositive() {
			acc, err := k.GetSummedAccrued(ctx)
			if err != nil {
				return err
			}
			if err := k.SummedAccrued.Set(ctx, acc.Sub(forfeited)); err != nil {
				return err
			}

			denom, err := k.HubDenom(ctx)
			if err != nil {
				return err
			}
			burned := sdk.NewCoins(sdk.NewCoin(denom, forfeited))
			if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, burned); err != nil {
				return err
			}
			if err := k.burnRecorder.RecordBurn(ctx, earthtypes.SourceAllocation, burned); err != nil {
				return err
			}
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"prune_allocation_option",
			sdk.NewAttribute("stream", stream.String()),
			sdk.NewAttribute("option_id", strconv.FormatUint(id, 10)),
			sdk.NewAttribute("recipient", opt.Recipient),
			sdk.NewAttribute("forfeited", forfeited.String()),
		))
	}

	if capped {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"prune_allocation_option_capped",
			sdk.NewAttribute("limit", strconv.Itoa(types.OptionPruneSweepLimit)),
		))
	}
	return nil
}
