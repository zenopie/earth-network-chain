package keeper

import (
	"context"
	"errors"
	"fmt"
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
//     matured records' payouts not yet made (payout - paid). Exact: every uerth the module receives is
//     booked in the same call (rewards are booked from balance deltas).
//  2. derth is never a coin (the module holds none), and
//     the derth locked in v's positions is at most derth_supply_v (the rest
//     is in stake notes, whose amounts are hidden).
//  3. Unbonding: per validator, the UNBONDING records' undelegated sum ==
//     the module's SDK entries' initial balances, and each record's creation
//     height has an entry.
//  4. Rate: per validator, pending_undelegation == its PENDING records'
//     targets, D + W + P >= U (the undelegations booked this epoch can be paid), and
//     derth_supply_v x rate_v == D + W + P - U. Tolerance: rate_v is an
//     18-decimal LegacyDec, so the product may fall short of the backing by
//     at most ceil(supply x 1e-18) + 1 uerth; it never exceeds it.
//     Conversions themselves are exact integer floors that favour the pool.
//  5. Reward escrows: every validator has its escrow recorded and its
//     operator's withdraw address is that escrow; no other escrow is
//     recorded (a removed validator's was released). The module account's
//     own withdraw address is itself (audit 4, G2).
//  6. Groundworks: per (validator, option), the stored total (current epoch)
//     == the sum of derth x percent over the validator's live positions.
//  7. Stake nullifier tree (O(1)): its size is 0, or 1 + its last value's
//     leaf index; the recorded latest size is at most the size.
//  8. Payouts: each record's queued payouts sum to its outstanding (and a
//     record with undelegations is never without them); every payout is in
//     exactly one queue: untried under its record, or failed under its
//     retry time; every MATURED record with untried payouts is marked for
//     the sweep.
//  9. Redelegations: every x/staking redelegation is this module's,
//     between two different validators, with at least one entry and at most
//     MaxEntryHeightsPerPair of positive height, in creation-height order,
//     every unmatured entry of positive height (known by height and
//     completion) holding exactly its moves' shares (checkRedelegationRecord,
//     as at genesis); no unbonding delegation is left set aside
//     (ShelteredUnbondings is empty outside BeginBlock).
//  10. Slash debt: every unmatured move is one an unmatured entry of its
//     (src, dst) holds, at its height with its completion (counted from the
//     records' side in 9: each record is decoded once); its retained is its
//     debt row's, or its credit when it has none; the debt tree's size is 0
//     or 1 + its last leaf index, with one key and one retained value per
//     leaf; no slash is left watched outside BeginBlock.
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
	if err := k.assertRates(ctx); err != nil {
		return err
	}
	if err := k.assertGwTotals(ctx); err != nil {
		return err
	}
	if err := k.assertNfTree(ctx); err != nil {
		return err
	}
	if err := k.assertPayouts(ctx); err != nil {
		return err
	}
	if err := k.assertRedelegations(ctx); err != nil {
		return err
	}
	if err := k.assertDebt(ctx); err != nil {
		return err
	}
	return k.assertEscrows(ctx)
}

func (k Keeper) assertDebt(ctx context.Context) error {
	if has, err := k.WatchSrc.Has(ctx); err != nil {
		return err
	} else if has {
		return types.ErrInvariant.Wrap("a slash is still watched after its block's start")
	}
	t, err := k.debtTree(ctx)
	if err != nil {
		return err
	}
	var leaves, idx, rows uint64
	last := uint64(0)
	if err := k.DebtLeafKeys.Walk(ctx, nil, func(i uint64, key []byte) (bool, error) {
		leaves++
		last = i
		if j, err := k.DebtIndex.Get(ctx, key); err != nil || j != i {
			return true, types.ErrInvariant.Wrapf("debt leaf %d: its key %X does not index it", i, key)
		}
		if _, err := k.DebtRetained.Get(ctx, key); err != nil {
			return true, types.ErrInvariant.Wrapf("debt leaf %d: no retained value", i)
		}
		return false, nil
	}); err != nil {
		return err
	}
	if err := k.DebtIndex.Walk(ctx, nil, func([]byte, uint64) (bool, error) { idx++; return false, nil }); err != nil {
		return err
	}
	if err := k.DebtRetained.Walk(ctx, nil, func([]byte, uint64) (bool, error) { rows++; return false, nil }); err != nil {
		return err
	}
	want := uint64(0)
	if leaves > 0 {
		want = last + 1
	}
	if t.Size() != want || idx != leaves || rows != leaves {
		return types.ErrInvariant.Wrapf("debt tree: size %d (want %d), %d leaves, %d keys, %d rows", t.Size(), want, leaves, idx, rows)
	}
	return k.Moves.Walk(ctx, nil, func(key []byte, mv types.Move) (bool, error) {
		r, err := k.DebtRetained.Get(ctx, key)
		switch {
		case err == nil && !mv.Retained.Equal(math.NewIntFromUint64(r)):
			return true, types.ErrInvariant.Wrapf("move %X: retained %s, its debt row %d", key, mv.Retained, r)
		case errors.Is(err, collections.ErrNotFound) && !mv.Retained.Equal(mv.Credited):
			return true, types.ErrInvariant.Wrapf("move %X: cut to %s without a debt row", key, mv.Retained)
		case err != nil && !errors.Is(err, collections.ErrNotFound):
			return true, err
		}
		// That it is in an unmatured entry of its (src, dst) is checked
		// with the redelegations (assertRedelegations: one decode a record).
		return false, nil
	})
}

func (k Keeper) assertNfTree(ctx context.Context) error {
	t, err := k.nfTree(ctx)
	if err != nil {
		return err
	}
	want := uint64(0) // 0, or the last value's leaf index + 1
	it, err := k.StakeNfValues.Iterate(ctx, new(collections.Range[uint64]).Descending())
	if err != nil {
		return err
	}
	if it.Valid() {
		last, err := it.Key()
		if err != nil {
			it.Close()
			return err
		}
		want = last + 1
	}
	it.Close()
	if t.Size() != want {
		return types.ErrInvariant.Wrapf("stake nullifier tree: size %d, want %d", t.Size(), want)
	}
	_, latest, err := k.latestNfRoot(ctx)
	if err != nil {
		return err
	}
	if latest > t.Size() {
		return types.ErrInvariant.Wrapf("stake nullifier tree: recorded size %d above size %d", latest, t.Size())
	}
	return nil
}

func (k Keeper) assertEscrows(ctx context.Context) error {
	if wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, k.modAddr); err != nil {
		return err
	} else if !wa.Equals(k.modAddr) {
		return types.ErrInvariant.Wrapf("module withdraw address %s is not the module account", wa)
	}
	vals, err := k.staking.GetAllValidators(ctx)
	if err != nil {
		return err
	}
	for _, v := range vals {
		val, err := k.staking.ValidatorAddressCodec().StringToBytes(v.GetOperator())
		if err != nil {
			return err
		}
		escrow := types.RewardEscrowAddress(val)
		owner, ok, err := k.escrowOwner(ctx, escrow)
		if err != nil {
			return err
		}
		if !ok || !sdk.ValAddress(owner).Equals(sdk.ValAddress(val)) {
			return types.ErrInvariant.Wrapf("validator %s: reward escrow %s not recorded", v.GetOperator(), escrow)
		}
		wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, sdk.AccAddress(val))
		if err != nil {
			return err
		}
		if !wa.Equals(escrow) {
			return types.ErrInvariant.Wrapf("validator %s: operator withdraw address %s is not its reward escrow %s", v.GetOperator(), wa, escrow)
		}
	}
	// A removed validator's escrow whose release failed stays recorded
	// until the retry succeeds (PendingReleases).
	n := 0
	if err := k.RewardEscrows.Walk(ctx, nil, func(_, owner []byte) (bool, error) {
		pending, err := k.PendingReleases.Has(ctx, owner)
		if err != nil {
			return true, err
		}
		if !pending {
			n++
		}
		return false, nil
	}); err != nil {
		return err
	}
	if n != len(vals) {
		return types.ErrInvariant.Wrapf("%d reward escrows recorded for %d validators", n, len(vals))
	}
	return nil
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
	// derth is stake notes and book entries, never a coin: no account may
	// hold one (none can be minted).
	for _, c := range k.bank.GetAllBalances(ctx, k.modAddr) {
		if strings.HasPrefix(c.Denom, types.DerthPrefix) {
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

func (k Keeper) assertPayouts(ctx context.Context) error {
	// Keyed by validator/epoch: collections.Pair holds pointers.
	owed := map[string]math.Int{}
	if err := k.UnbondPayouts.Walk(ctx, nil, func(id uint64, p types.UnbondPayout) (bool, error) {
		rec := fmt.Sprintf("%s/%d", p.Validator, p.Epoch)
		if cur, ok := owed[rec]; ok {
			owed[rec] = cur.Add(p.Value)
		} else {
			owed[rec] = p.Value
		}
		first, err := k.PayoutsByRecord.Has(ctx, collections.Join3(p.Validator, p.Epoch, id))
		if err != nil {
			return true, err
		}
		retry, err := k.PayoutRetries.Has(ctx, collections.Join(p.RetryAt, id))
		if err != nil {
			return true, err
		}
		if first == retry || first != (p.RetryAt == 0) {
			return true, types.ErrInvariant.Wrapf("payout %d: queued untried %v, for retry %v (retry_at %d)", id, first, retry, p.RetryAt)
		}
		return false, nil
	}); err != nil {
		return err
	}
	n := 0
	if err := k.PayoutsByRecord.Walk(ctx, nil, func(key collections.Triple[string, uint64, uint64]) (bool, error) {
		n++
		p, err := k.UnbondPayouts.Get(ctx, key.K3())
		if err != nil {
			return true, types.ErrInvariant.Wrapf("queued payout %d: %v", key.K3(), err)
		}
		if p.Validator != key.K1() || p.Epoch != key.K2() {
			return true, types.ErrInvariant.Wrapf("payout %d queued under %s/%d", key.K3(), key.K1(), key.K2())
		}
		return false, nil
	}); err != nil {
		return err
	}
	if err := k.PayoutRetries.Walk(ctx, nil, func(key collections.Pair[int64, uint64]) (bool, error) {
		n++
		if ok, err := k.UnbondPayouts.Has(ctx, key.K2()); err != nil || !ok {
			return true, types.ErrInvariant.Wrapf("retry of payout %d with no payout", key.K2())
		}
		return false, nil
	}); err != nil {
		return err
	}
	total := 0
	if err := k.UnbondPayouts.Walk(ctx, nil, func(uint64, types.UnbondPayout) (bool, error) {
		total++
		return false, nil
	}); err != nil {
		return err
	}
	if n != total {
		return types.ErrInvariant.Wrapf("%d payout queue entries for %d payouts", n, total)
	}
	return k.UnbondRecords.Walk(ctx, nil, func(key collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		o, ok := owed[fmt.Sprintf("%s/%d", key.K1(), key.K2())]
		if !ok {
			o = math.ZeroInt()
		}
		if r.Requested.IsPositive() && !o.Equal(r.Outstanding) {
			return true, types.ErrInvariant.Wrapf("record %s/%d: outstanding %s, payouts %s", key.K1(), key.K2(), r.Outstanding, o)
		}
		if r.Status == types.UNBOND_STATUS_MATURED {
			untried := false
			if err := k.PayoutsByRecord.Walk(ctx, collections.NewSuperPrefixedTripleRange[string, uint64, uint64](key.K1(), key.K2()),
				func(collections.Triple[string, uint64, uint64]) (bool, error) {
					untried = true
					return true, nil
				}); err != nil {
				return true, err
			}
			if marked, err := k.MaturedRecords.Has(ctx, key); err != nil {
				return true, err
			} else if untried && !marked {
				return true, types.ErrInvariant.Wrapf("matured record %s/%d has untried payouts but is not marked", key.K1(), key.K2())
			}
		}
		return false, nil
	})
}

func (k Keeper) assertRedelegations(ctx context.Context) error {
	sheltered := 0
	if err := k.ShelteredUnbondings.Walk(ctx, nil, func([]byte, stakingtypes.UnbondingDelegation) (bool, error) {
		sheltered++
		return false, nil
	}); err != nil {
		return err
	}
	if sheltered > 0 {
		return types.ErrInvariant.Wrapf("%d unbonding delegations still set aside after a slash", sheltered)
	}
	var bad error
	held := 0
	if err := k.staking.IterateRedelegations(ctx, func(_ int64, r stakingtypes.Redelegation) bool {
		n, err := k.checkRedelegationRecord(ctx, r)
		if err != nil {
			bad = types.ErrInvariant.Wrap(err.Error())
		}
		held += n
		return bad != nil
	}); err != nil {
		return err
	}
	if bad != nil {
		return bad
	}
	// Invariant 10's other half: each record is decoded once, here, and every
	// unmatured move must be in one of their entries (audit 7, A7-L1).
	if err := k.checkOpenMoves(ctx, held); err != nil {
		return types.ErrInvariant.Wrap(err.Error())
	}
	return nil
}
