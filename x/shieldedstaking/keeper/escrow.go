package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Reward escrows (see withdraw_addr.go for why). Each validator has one,
// types.RewardEscrowAddress(val), recorded in RewardEscrows from its
// creation to its removal. Coins enter only from x/distribution (the
// operator's self-bond rewards, the validator's commission) and leave only
// to the operator, moved by this module (SendRestriction):
//
//   - epoch end, active (bonded, unjailed) validator: the escrow's uerth goes
//     to the operator account and is self-delegated in the same cache
//     context (compoundSelfBond), so it is never liquid;
//   - a jailed validator's escrow keeps accruing (what self-bond changes pay
//     it) until the validator is active again;
//   - the validator is removed (x/staking removes it once it is unbonded and
//     holds no delegation: its operator undelegated the whole self-bond and
//     the unbonding period passed): everything left in the escrow — every
//     denom — is released to the operator (releaseEscrow). That is the
//     operator's exit, after unbonding, as for the self-bond itself;
//   - the operator removed its whole self-bond and the unbonding time passed
//     (it retired) while the validator lives on because private derth is
//     still delegated to it (audit F7: dust nobody undelegates would freeze
//     the escrow for ever): the escrow's balance is paid to the operator
//     (releaseRetiredEscrows) and the escrow stays recorded, since the
//     validator still exists. Nothing in an escrow is slashable stake; the
//     wait is only so a retirement takes as long as a removal.
//
// Non-uerth rewards (fee denoms) cannot be staked: they stay in the escrow
// until the validator is removed.
//
// Only the escrow's spendable coins ever move (audit 3): the address is
// derivable, so anyone can make it an account before the validator exists --
// a permanently locked vesting account holding 1 uerth, say. Locked coins are
// not the operator's and stay where they are; they never fail a compounding
// or a release. Such an account is not refused at MsgCreateValidator either:
// anyone could then block any future validator by poisoning its escrow.

// escrowOwner reports whether addr is a validator's reward escrow, and
// whose.
func (k Keeper) escrowOwner(ctx context.Context, addr sdk.AccAddress) (sdk.ValAddress, bool, error) {
	val, err := k.RewardEscrows.Get(ctx, addr)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

// scheduleRetirement records that val's operator removed its whole
// self-bond: once the unbonding time has passed, releaseRetiredEscrows pays
// the escrow out if the operator has not bonded again.
func (k Keeper) scheduleRetirement(ctx context.Context, val sdk.ValAddress) error {
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return err
	}
	at := sdk.UnwrapSDKContext(ctx).BlockTime().Add(ut).UnixNano()
	return k.RetiringEscrows.Set(ctx, collections.Join(at, []byte(val)))
}

// releaseRetiredEscrows pays out the escrows of operators whose retirement
// is due (bounded per block): the operator holds no self-delegation and no
// unbonding self-delegation at val. An operator that bonded again is
// forgotten (its next removal schedules it anew); one still unbonding is
// looked at again just after its latest unbonding entry completes.
func (k Keeper) releaseRetiredEscrows(ctx context.Context) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	var due []collections.Pair[int64, []byte]
	_ = k.RetiringEscrows.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if key.K1() > now || len(due) >= types.EscrowRetireLimit {
			return true, nil
		}
		due = append(due, key)
		return false, nil
	})
	for _, key := range due {
		val := sdk.ValAddress(key.K2())
		err := k.guarded(ctx, func(cc context.Context) error {
			if err := k.RetiringEscrows.Remove(cc, key); err != nil {
				return err
			}
			if _, ok, err := k.escrowOwner(cc, types.RewardEscrowAddress(val)); err != nil || !ok {
				return err // removed meanwhile: AfterValidatorRemoved released it
			}
			op := sdk.AccAddress(val)
			if _, err := k.staking.GetDelegation(cc, op, val); err == nil {
				return nil // bonded again
			} else if !errors.Is(err, stakingtypes.ErrNoDelegation) {
				return err
			}
			if ubd, err := k.staking.GetUnbondingDelegation(cc, op, val); err == nil && len(ubd.Entries) > 0 {
				// Looked at again once its last entry has matured, not
				// every block: an operator that re-bonded and unbonded
				// after retiring would otherwise hold a slot of the
				// per-block budget for a whole unbonding period (audit C-3).
				at := now + 1
				for _, e := range ubd.Entries {
					if c := e.CompletionTime.UnixNano(); c >= at {
						at = c + 1
					}
				}
				return k.RetiringEscrows.Set(cc, collections.Join(at, key.K2()))
			} else if err != nil && !errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
				return err
			}
			return k.payEscrow(cc, val)
		})
		if err != nil {
			k.failure(ctx, "escrow_retire", val.String(), err)
			// Behind everything due now: an entry that keeps failing must
			// not hold the head of the queue (and the per-block budget).
			retry := sdk.UnwrapSDKContext(ctx).BlockTime().Add(types.EscrowRetryDelay).UnixNano()
			_ = k.guarded(ctx, func(cc context.Context) error {
				if err := k.RetiringEscrows.Remove(cc, key); err != nil {
					return err
				}
				return k.RetiringEscrows.Set(cc, collections.Join(retry, key.K2()))
			})
		}
	}
}

// payEscrow pays everything spendable in val's reward escrow to its
// operator. Coins locked at the escrow address (an account someone made
// there) stay.
func (k Keeper) payEscrow(ctx context.Context, val sdk.ValAddress) error {
	op, escrow := sdk.AccAddress(val), types.RewardEscrowAddress(val)
	if bal := k.bank.SpendableCoins(ctx, escrow); !bal.IsZero() {
		if err := k.bank.SendCoins(ctx, escrow, op, bal); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEscrowReleased,
			sdk.NewAttribute(types.AttributeKeyValidator, val.String()),
			sdk.NewAttribute(types.AttributeKeyAmount, bal.String()),
		))
	}
	return nil
}

// retryEscrowReleases retries, at each epoch end, the releases of removed
// validators' escrows that failed when they were removed. Each round starts
// after the last entry the previous one tried (wrapping around), so entries
// that keep failing cannot take the whole budget for ever.
func (k Keeper) retryEscrowReleases(ctx context.Context) {
	cursor, err := k.PendingReleaseCursor.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		k.failure(ctx, "escrow_release", "", err)
		return
	}
	var vals [][]byte
	seen := map[string]bool{}
	collect := func(rng collections.Ranger[[]byte]) {
		_ = k.PendingReleases.Walk(ctx, rng, func(v []byte) (bool, error) {
			if seen[string(v)] {
				return true, nil
			}
			seen[string(v)] = true
			vals = append(vals, v)
			return len(vals) >= types.EscrowRetireLimit, nil
		})
	}
	if len(cursor) > 0 {
		collect(new(collections.Range[[]byte]).StartExclusive(cursor))
	}
	if len(vals) < types.EscrowRetireLimit {
		collect(nil) // wrap around
	}
	if len(vals) == 0 {
		return
	}
	if err := k.PendingReleaseCursor.Set(ctx, vals[len(vals)-1]); err != nil {
		k.failure(ctx, "escrow_release", "", err)
		return
	}
	for _, v := range vals {
		err := k.guarded(ctx, func(cc context.Context) error {
			if err := k.releaseEscrow(cc, v); err != nil {
				return err
			}
			return k.PendingReleases.Remove(cc, v)
		})
		if err != nil {
			k.failure(ctx, "escrow_release", sdk.ValAddress(v).String(), err)
		}
	}
}

// releaseEscrow pays everything in val's reward escrow to its operator,
// forgets the escrow and resets the operator's withdraw address to itself:
// the validator is gone.
func (k Keeper) releaseEscrow(ctx context.Context, val sdk.ValAddress) error {
	op, escrow := sdk.AccAddress(val), types.RewardEscrowAddress(val)
	if err := k.payEscrow(ctx, val); err != nil {
		return err
	}
	if err := k.RewardEscrows.Remove(ctx, escrow); err != nil {
		return err
	}
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, op)
	if err != nil {
		return err
	}
	if wa.Equals(escrow) {
		return k.distr.DeleteDelegatorWithdrawAddr(ctx, op, wa)
	}
	return nil
}
