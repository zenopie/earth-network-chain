package keeper

import (
	"context"
	"errors"
	"sort"
	"strings"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// AssertInvariants checks private staking's books against the bank and
// x/staking. The epoch end runs it and reports (never halts) on a failure;
// tests and genesis run it directly.
//
//  1. ERTH: the module's uerth balance == sum of queued delegations + the
//     matured, unclaimed payouts. Exact: every uerth the module receives is
//     booked in the same call (rewards are booked from balance deltas).
//  2. derth and unbond claims are never coins (the module holds none), and
//     the derth locked in v's positions is at most derth_supply_v (the rest
//     is in stake notes, whose amounts are hidden).
//  3. Unbonding: per validator, the UNBONDING records' undelegated sum ==
//     the module's SDK entries' initial balances, and each record's creation
//     height has an entry.
//  4. Rate: per validator, pending_undelegation == its PENDING records'
//     targets, D + W + P >= U (the notes minted this epoch can be paid), and
//     derth_supply_v x rate_v == D + W + P - U. Tolerance: rate_v is an
//     18-decimal LegacyDec, so the product may fall short of the backing by
//     at most ceil(supply x 1e-18) + 1 uerth; it never exceeds it.
//     Conversions themselves are exact integer floors that favour the pool.
func (k Keeper) AssertInvariants(ctx context.Context) error {
	if err := k.assertERTH(ctx); err != nil {
		return err
	}
	if err := k.assertDenoms(ctx); err != nil {
		return err
	}
	if err := k.assertUnbonding(ctx); err != nil {
		return err
	}
	return k.assertRates(ctx)
}

func (k Keeper) assertERTH(ctx context.Context) error {
	owed := math.ZeroInt()
	if err := k.Validators.Walk(ctx, nil, func(_ string, vs types.ValidatorState) (bool, error) {
		owed = owed.Add(vs.PendingDelegation)
		return false, nil
	}); err != nil {
		return err
	}
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		if r.Status == types.UNBOND_STATUS_MATURED {
			owed = owed.Add(r.Payout.Sub(r.Paid))
		}
		return false, nil
	}); err != nil {
		return err
	}
	held := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
	// Entries maturing in this block are recorded MATURED already (this
	// module's EndBlocker runs first) but x/staking pays them after.
	incoming, err := k.maturingNow(ctx)
	if err != nil {
		return err
	}
	if !held.Add(incoming).Equal(owed) {
		return types.ErrInvariant.Wrapf("module holds %s uerth (+%s maturing), books say %s", held, incoming, owed)
	}
	return nil
}

// maturingNow is the balance of the module's SDK unbonding entries that are
// mature at this block's time and not yet paid.
func (k Keeper) maturingNow(ctx context.Context) (math.Int, error) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	vals := map[string]bool{}
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		vals[r.Validator] = true
		return false, nil
	}); err != nil {
		return math.Int{}, err
	}
	sum := math.ZeroInt()
	for _, v := range sortedKeys(vals) {
		val, err := k.valAddr(v)
		if err != nil {
			return math.Int{}, err
		}
		ubd, err := k.staking.GetUnbondingDelegation(ctx, k.modAddr, val)
		if errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
			continue
		} else if err != nil {
			return math.Int{}, err
		}
		for _, e := range ubd.Entries {
			if e.IsMature(now) {
				sum = sum.Add(e.Balance)
			}
		}
	}
	return sum, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (k Keeper) assertDenoms(ctx context.Context) error {
	// derth and unbond claims are stake notes and book entries, never coins:
	// no account may hold one (none can be minted).
	for _, c := range k.bank.GetAllBalances(ctx, k.modAddr) {
		if strings.HasPrefix(c.Denom, types.UnbondPrefix) || strings.HasPrefix(c.Denom, types.DerthPrefix) {
			return types.ErrInvariant.Wrapf("module holds %s", c)
		}
	}
	locked := map[string]math.Int{}
	if err := k.Positions.Walk(ctx, nil, func(_ uint64, p types.Position) (bool, error) {
		if cur, ok := locked[p.Validator]; ok {
			locked[p.Validator] = cur.Add(p.Derth)
		} else {
			locked[p.Validator] = p.Derth
		}
		return false, nil
	}); err != nil {
		return err
	}
	for v, l := range locked {
		if s := k.Supply(ctx, v); l.GT(s) {
			return types.ErrInvariant.Wrapf("positions lock %s derth/%s, more than its supply %s", l, v, s)
		}
	}
	return nil
}

func (k Keeper) assertUnbonding(ctx context.Context) error {
	booked := map[string]math.Int{}
	heights := map[string]map[int64]bool{}
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		if r.Status != types.UNBOND_STATUS_UNBONDING {
			return false, nil
		}
		if cur, ok := booked[r.Validator]; ok {
			booked[r.Validator] = cur.Add(r.Undelegated)
		} else {
			booked[r.Validator] = r.Undelegated
			heights[r.Validator] = map[int64]bool{}
		}
		heights[r.Validator][r.CreationHeight] = true
		return false, nil
	}); err != nil {
		return err
	}
	for v, want := range booked {
		val, err := k.valAddr(v)
		if err != nil {
			return err
		}
		ubd, err := k.staking.GetUnbondingDelegation(ctx, k.modAddr, val)
		if err != nil && !errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
			return err
		}
		got := math.ZeroInt()
		seen := map[int64]bool{}
		for _, e := range ubd.Entries {
			if e.IsMature(sdk.UnwrapSDKContext(ctx).BlockTime()) {
				continue // matured this block; x/staking pays it next
			}
			got = got.Add(e.InitialBalance)
			seen[e.CreationHeight] = true
		}
		if !got.Equal(want) {
			return types.ErrInvariant.Wrapf("%s: unbonding records book %s, SDK entries hold %s", v, want, got)
		}
		for h := range heights[v] {
			if !seen[h] {
				return types.ErrInvariant.Wrapf("%s: no SDK entry at height %d", v, h)
			}
		}
	}
	return nil
}

func (k Keeper) assertRates(ctx context.Context) error {
	var vals []types.ValidatorState
	_ = k.Validators.Walk(ctx, nil, func(_ string, vs types.ValidatorState) (bool, error) {
		vals = append(vals, vs)
		return false, nil
	})
	for _, vs := range vals {
		records, err := k.pendingRecords(ctx, vs.Validator)
		if err != nil {
			return err
		}
		t := math.ZeroInt()
		for _, r := range records {
			t = t.Add(r.Target)
		}
		if !t.Equal(vs.PendingUndelegation) {
			return types.ErrInvariant.Wrapf("%s: pending undelegation %s, records %s", vs.Validator, vs.PendingUndelegation, t)
		}
		val, err := k.valAddr(vs.Validator)
		if err != nil {
			return err
		}
		d, del, v, found, err := k.delegation(ctx, val)
		if err != nil {
			return err
		}
		w := math.ZeroInt()
		if found && del.Shares.IsPositive() {
			if w, err = k.pendingRewards(ctx, v, del); err != nil {
				return err
			}
		}
		gross := d.Add(w).Add(vs.PendingDelegation)
		if gross.LT(vs.PendingUndelegation) {
			return types.ErrInvariant.Wrapf("%s: %s pending undelegation exceeds the %s behind it", vs.Validator, vs.PendingUndelegation, gross)
		}
		backing := gross.Sub(vs.PendingUndelegation)
		s := k.Supply(ctx, vs.Validator)
		if !s.IsPositive() {
			continue
		}
		implied := rateOf(backing, s).MulInt(s).TruncateInt()
		tol := s.Quo(math.NewIntWithDecimal(1, 18)).AddRaw(2)
		if implied.GT(backing) || backing.Sub(implied).GT(tol) {
			return types.ErrInvariant.Wrapf("%s: supply x rate = %s, backing %s", vs.Validator, implied, backing)
		}
	}
	return nil
}
