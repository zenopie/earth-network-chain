package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Stake vintages (v1.3.0; ORCHARD_DESIGN.md 8.9).
//
// Every stake note's asset names its vintage: the UTC month its stake was
// last checked in (types.VintageDenom). The chain keeps each validator's
// supply per vintage, S_v = sum over m of S_{v,m}: every derth creation and
// destruction already has a public amount, and is booked to the vintage it
// names (a delegation, a redelegation's credit and a check-in's credit to
// the current month; an undelegation, a redelegation's source and a
// check-in's source to the msg's vintage; a slash's debt to the vintage the
// move's credit landed in). The pot and the rate stay one per validator:
// B_v / S_v over every live vintage.
//
// A vintage is live in its own month and the 12 after it. At 00:00 UTC on
// the 1st of month m + 13 vintage m retires: from then on every msg on its
// asset but a withdrawal is refused (by block time), and in the first
// block at or after that time the monthly pass (retireVintages,
// BeginBlock; bounded per block, catching up every month a halt skipped)
// drops its Groundworks totals. Its supply stays in S_v, earning at the
// rate, until the next epoch end reaches its book (retireDue, right before
// processValidator): there it leaves the supply at the live rate, V =
// floor(S_{v,m} x B_v / S_v), booked as one private undelegation of the
// epoch that ended, held for the vintage (an UnbondPayout with no pc), and
// is undelegated in that same step with the epoch's other undelegations.
// Slashes cut it as they cut any (the SDK entry). So the retired value
// earns, for its own holders, exactly until it leaves x/staking, and no
// live holder can time a delegation to take its tail.
//
// A retired note withdraws its share: MsgUndelegate on the retired asset,
// v_out = d, no split (withdrawRetired): floor(d x remaining_value /
// remaining_supply) (all that remains for the last derth) is split off the
// held payout as an ordinary payout to the msg's pc, paid at the record's
// maturity, or in the next block when it has matured. Every other msg on a
// retired asset is refused (ErrRetiredVintage).
//
// MsgCheckIn moves a live, older vintage's derth into the current vintage
// (S_{v,old} -> S_{v,now}): no value moves and the rate does not change.

// vintageCtx is the base vintage and the block's month.
func (k Keeper) vintageCtx(ctx context.Context) (types.VintageCtx, error) {
	base, err := k.baseVintage(ctx)
	if err != nil {
		return types.VintageCtx{}, err
	}
	return types.NewVintageCtx(base, sdk.UnwrapSDKContext(ctx).BlockTime()), nil
}

// baseVintage is the vintage whose denom is the unsuffixed derth/<valoper>,
// set once by InitGenesis or the v1.3.0 upgrade. Unset is an error, never a
// fallback (audit 1, L5: falling back to the current month would rename
// every pre-v1.3.0 note's vintage each month, so it never retired).
func (k Keeper) baseVintage(ctx context.Context) (uint32, error) {
	b, err := k.BaseVintage.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return 0, types.ErrInvariant.Wrap("the base vintage is unset (InitGenesis or the v1.3.0 upgrade sets it)")
	}
	return b, err
}

// currentVintage is the block time's UTC month.
func (k Keeper) currentVintage(ctx context.Context) uint32 {
	return types.MonthOf(sdk.UnwrapSDKContext(ctx).BlockTime())
}

// StakeLanes is what the chain supplies to msg's stake proof now.
func (k Keeper) StakeLanes(ctx context.Context, msg types.StakeMsg) (types.StakeLanes, error) {
	vc, err := k.vintageCtx(ctx)
	if err != nil {
		return types.StakeLanes{}, err
	}
	return msg.StakeLanes(vc), nil
}

// VintageContext is the base vintage and the current month (for wallets'
// and tests' asset denoms).
func (k Keeper) VintageContext(ctx context.Context) (types.VintageCtx, error) {
	return k.vintageCtx(ctx)
}

// VintageSupply is S_{v,m}.
func (k Keeper) VintageSupply(ctx context.Context, valoper string, m uint32) (math.Int, error) {
	s, err := k.VintageSupplies.Get(ctx, collections.Join(valoper, m))
	if errors.Is(err, collections.ErrNotFound) {
		return math.ZeroInt(), nil
	}
	return s, err
}

// AddVintageSupply is addVintageSupply, for tools and tests that book
// derth by hand (the caller keeps the book's derth_supply equal).
func (k Keeper) AddVintageSupply(ctx context.Context, valoper string, m uint32, delta math.Int) error {
	return k.addVintageSupply(ctx, valoper, m, delta)
}

// CurrentVintage is the block time's UTC month.
func (k Keeper) CurrentVintage(ctx context.Context) uint32 { return k.currentVintage(ctx) }

// addVintageSupply adds delta to S_{v,m}, deleting it at zero; below zero
// is refused (more derth than the vintage holds).
func (k Keeper) addVintageSupply(ctx context.Context, valoper string, m uint32, delta math.Int) error {
	cur, err := k.VintageSupply(ctx, valoper, m)
	if err != nil {
		return err
	}
	next := cur.Add(delta)
	switch {
	case next.IsNegative():
		return errorsmod.Wrapf(types.ErrAmount, "vintage %d at %s holds %s derth, not %s", m, valoper, cur, delta.Neg())
	case next.IsZero():
		return k.VintageSupplies.Remove(ctx, collections.Join(valoper, m))
	default:
		return k.VintageSupplies.Set(ctx, collections.Join(valoper, m), next)
	}
}

// checkLive refuses a vintage that is not live in month cur: retired
// (ErrRetiredVintage) or not yet begun (ErrVintage).
func checkLive(m, cur uint32) error {
	if err := types.ValidateVintage(m); err != nil {
		return err
	}
	if types.VintageRetired(m, cur) {
		return types.ErrRetiredVintage.Wrapf("vintage %d retired at %s", m, types.VintageRetiresAt(m).Format("2006-01-02"))
	}
	if !types.VintageLive(m, cur) {
		return types.ErrVintage.Wrapf("vintage %d is after the current month %d", m, cur)
	}
	return nil
}

// checkLaneVintage resolves a msg's lane A vintage and refuses it unless
// it is live now.
func (k Keeper) checkLaneVintage(ctx context.Context, m types.StakeMsg) (uint32, error) {
	vc, err := k.vintageCtx(ctx)
	if err != nil {
		return 0, err
	}
	a, _ := m.LaneVintages(vc)
	return a, checkLive(a, vc.Current)
}

// ---- retirement --------------------------------------------------------------

// retireVintages is the BeginBlock monthly pass over the Groundworks
// voters. Once a month (the first block of a new UTC month, or after a
// halt the first block after it) it walks every book, VintageRetireLimit a
// block from a persistent cursor, dropping the totals of vintages older
// than the month's oldest live one and queueing the voter's re-file
// (GwResync, EndBlock), so a retired vintage stops voting at its boundary.
// A pass during which any book failed, in any of its blocks, runs again a
// day later (audit 1, L1: VintageSweep.failed carries a failure across the
// pass's blocks). Never fails.
//
// The retired vintage's VALUE leaves its book at the epoch end instead
// (retireDue): until then its supply stays in S_v and earns at the rate.
func (k Keeper) retireVintages(ctx context.Context) {
	cur := k.currentVintage(ctx)
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	sweep, err := k.VintageSweep.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		k.failure(ctx, "vintage_sweep", "", err)
		return
	}
	switch {
	case sweep.Month != cur:
		sweep = types.VintageSweep{Month: cur, Active: true}
	case !sweep.Active && sweep.RetryAt != 0 && now >= sweep.RetryAt:
		sweep = types.VintageSweep{Month: cur, Active: true}
	case !sweep.Active:
		return
	}
	var rng collections.Ranger[string]
	if sweep.Cursor != "" {
		rng = new(collections.Range[string]).StartExclusive(sweep.Cursor)
	}
	var vals []string
	if err := k.guarded(ctx, func(cc context.Context) error {
		return k.Validators.Walk(cc, rng, func(v string, _ types.ValidatorState) (bool, error) {
			vals = append(vals, v)
			return len(vals) >= types.VintageRetireLimit, nil
		})
	}); err != nil {
		// Retried in the next block, from the same cursor.
		k.failure(ctx, "vintage_sweep", sweep.Cursor, err)
		return
	}
	for _, v := range vals {
		if err := k.guarded(ctx, func(cc context.Context) error { return k.dropRetiredTotals(cc, v, cur) }); err != nil {
			k.failure(ctx, "vintage_totals", v, err)
			sweep.Failed = true
		}
	}
	if len(vals) < types.VintageRetireLimit {
		// The pass is done: again in a day if any of its blocks failed a book.
		sweep.Active, sweep.Cursor, sweep.RetryAt = false, "", 0
		if sweep.Failed {
			sweep.RetryAt = now + types.VintageRetryDelay
		}
		sweep.Failed = false
	} else {
		sweep.Cursor = vals[len(vals)-1]
	}
	if err := k.VintageSweep.Set(ctx, sweep); err != nil {
		k.failure(ctx, "vintage_sweep", "", err)
	}
}

// dropRetiredTotals drops valoper's Groundworks totals of vintages retired
// by month cur; a voter that changed is re-filed at the end of the block
// (GwResync), after x/allocation's BeginBlock has settled the stream.
func (k Keeper) dropRetiredTotals(ctx context.Context, valoper string, cur uint32) error {
	dropped, err := k.dropStaleTotals(ctx, valoper, cur)
	if err != nil || !dropped {
		return err
	}
	return k.GwResync.Set(ctx, valoper)
}

// retireDue books, at the epoch end, every vintage of valoper's book that
// has retired by now (oldest first: all a halt skipped). It runs right
// before processValidator (sweepBooks), which undelegates the book's
// records of epochs up to maxEpoch in the same block: the retired value is
// valued at the live rate in the step that takes it out of x/staking, so
// it earned in the book, for its own holders, until it left (audit 1, L3:
// valued at the boundary and undelegated up to an epoch later, its tail
// rewards fell to whoever held live derth at the epoch end, a fresh
// delegator to a wholly retired book included).
//
// Left for a later epoch end (still earning in the book) when x/staking
// would defer the undelegation (max_entries; it would wait in U, earning
// nothing) or when the record of maxEpoch is no longer pending.
func (k Keeper) retireDue(ctx context.Context, valoper string, maxEpoch uint64) error {
	cur := k.currentVintage(ctx)
	rng := collections.NewPrefixedPairRange[string, uint32](valoper).EndExclusive(types.OldestLiveVintage(cur))
	type due struct {
		m uint32
		s math.Int
	}
	var dues []due
	if err := k.VintageSupplies.Walk(ctx, rng, func(key collections.Pair[string, uint32], s math.Int) (bool, error) {
		dues = append(dues, due{key.K2(), s})
		return false, nil
	}); err != nil {
		return err
	}
	if len(dues) == 0 {
		return nil
	}
	val, err := k.valAddr(valoper)
	if err != nil {
		return err
	}
	if full, err := k.unbondingEntriesFull(ctx, val); err != nil || full {
		return err
	}
	if r, err := k.UnbondRecords.Get(ctx, collections.Join(valoper, maxEpoch)); err == nil && r.Status != types.UNBOND_STATUS_PENDING {
		return nil
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	for _, d := range dues {
		if err := k.retireVintage(ctx, valoper, d.m, d.s, maxEpoch); err != nil {
			return err
		}
	}
	return nil
}

// retireVintage takes vintage m's supply s out of valoper's book at the
// live rate and books its value V = floor(s x B / S) as a held
// undelegation of epoch (the record's requested, target and outstanding,
// the book's pending undelegation; an UnbondPayout with no pc), recording
// the RetiredVintage the vintage's notes withdraw from.
func (k Keeper) retireVintage(ctx context.Context, valoper string, m uint32, s math.Int, epoch uint64) error {
	key := collections.Join(valoper, m)
	if has, err := k.RetiredVintages.Has(ctx, key); err != nil {
		return err
	} else if has {
		// A second held payout would orphan the first (audit 1, L6).
		return types.ErrInvariant.Wrapf("vintage %d at %s holds supply and has retired already", m, valoper)
	}
	b, S, err := k.Backing(ctx, valoper)
	if err != nil {
		return err
	}
	v := math.ZeroInt()
	if S.IsPositive() {
		if s.GT(S) {
			return types.ErrInvariant.Wrapf("vintage %d at %s holds %s derth, the book %s", m, valoper, s, S)
		}
		v = s.Mul(b).Quo(S)
	}
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return err
	}
	if err := k.checkpointSupply(ctx, &vs); err != nil {
		return err
	}
	vs.DerthSupply = vs.DerthSupply.Sub(s)
	if vs.DerthSupply.IsNegative() {
		return types.ErrInvariant.Wrapf("%s: retiring vintage %d takes the supply below zero", valoper, m)
	}
	rv := types.RetiredVintage{
		Validator: valoper, Vintage: m, Supply: s, Value: v, Epoch: epoch,
		RemainingSupply: s, RemainingValue: v, RetiredAt: sdk.UnwrapSDKContext(ctx).BlockTime().Unix(),
	}
	if v.IsPositive() {
		rkey := collections.Join(valoper, epoch)
		r, err := k.UnbondRecords.Get(ctx, rkey)
		if errors.Is(err, collections.ErrNotFound) {
			r = types.UnbondRecord{
				Validator: valoper, Epoch: epoch, Status: types.UNBOND_STATUS_PENDING,
				Requested: math.ZeroInt(), Target: math.ZeroInt(), Undelegated: math.ZeroInt(),
				Payout: math.ZeroInt(), Outstanding: math.ZeroInt(), Paid: math.ZeroInt(),
			}
		} else if err != nil {
			return err
		} else if r.Status != types.UNBOND_STATUS_PENDING {
			return types.ErrNotMatured.Wrapf("%s/%d is already %s", valoper, epoch, r.Status)
		}
		r.Requested = r.Requested.Add(v)
		r.Target = r.Target.Add(v)
		r.Outstanding = r.Outstanding.Add(v)
		if err := k.UnbondRecords.Set(ctx, rkey, r); err != nil {
			return err
		}
		if err := k.PendingRecords.Set(ctx, rkey); err != nil {
			return err
		}
		vs.PendingUndelegation = vs.PendingUndelegation.Add(v)
		id, err := k.UnbondPayoutSeq.Next(ctx)
		if err != nil {
			return err
		}
		// Held: queued nowhere, so the sweep never pays it; withdrawals
		// split it (withdrawRetired).
		if err := k.UnbondPayouts.Set(ctx, id, types.UnbondPayout{Id: id, Validator: valoper, Epoch: epoch, Value: v}); err != nil {
			return err
		}
		rv.PayoutId = id
	}
	if err := k.Validators.Set(ctx, valoper, vs); err != nil {
		return err
	}
	if err := k.VintageSupplies.Remove(ctx, key); err != nil {
		return err
	}
	if err := k.RetiredVintages.Set(ctx, key, rv); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeVintageRetired,
		sdk.NewAttribute(types.AttributeKeyValidator, valoper),
		sdk.NewAttribute(types.AttributeKeyVintage, strconv.FormatUint(uint64(m), 10)),
		sdk.NewAttribute(types.AttributeKeySupply, s.String()),
		sdk.NewAttribute(types.AttributeKeyValue, v.String()),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(epoch, 10)),
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(rv.PayoutId, 10)),
	))
	return nil
}

// resyncRetired re-files, in EndBlock, the voters whose totals a
// retirement dropped this block. Never fails.
func (k Keeper) resyncRetired(ctx context.Context) {
	var vals []string
	if err := k.GwResync.Walk(ctx, nil, func(v string) (bool, error) {
		vals = append(vals, v)
		return false, nil
	}); err != nil {
		// What was read is re-filed; the rest stays queued for the next block.
		k.failure(ctx, "vintage_resync", "", err)
	}
	for _, v := range vals {
		k.resyncValidatorVoter(ctx, v)
		if err := k.GwResync.Remove(ctx, v); err != nil {
			k.failure(ctx, "vintage_resync", v, err)
		}
	}
}

// sweepDeadVotes deletes, in BeginBlock, up to DeadVoteSweepLimit
// Groundworks votes of vintages retired by now (oldest first). They count
// for nothing already (their totals went with the retirement); a note's
// withdrawal deletes its own sooner. Never fails, and never stalls on one
// entry (audit 1, L4): an index entry whose vote is gone, or whose vote
// is stored under another vintage or validator, is dropped; a vote whose
// removal fails is reported (event shieldedstaking_epoch_failure, stage
// dead_votes, its validator, "vote <id> parked" in the error) and parked: its index entry is dropped so the
// sweep moves past it, and the vote itself stays, counting for nothing,
// until its note is withdrawn (or a genesis import re-indexes it).
func (k Keeper) sweepDeadVotes(ctx context.Context) {
	oldest := types.OldestLiveVintage(k.currentVintage(ctx))
	rng := new(collections.Range[collections.Triple[uint32, string, uint64]]).
		EndExclusive(collections.TriplePrefix[uint32, string, uint64](oldest))
	var dead []collections.Triple[uint32, string, uint64]
	if err := k.GwVotesByVintage.Walk(ctx, rng, func(key collections.Triple[uint32, string, uint64]) (bool, error) {
		dead = append(dead, key)
		return len(dead) >= types.DeadVoteSweepLimit, nil
	}); err != nil {
		k.failure(ctx, "dead_votes", "", err)
		return
	}
	for _, key := range dead {
		id := key.K3()
		err := k.guarded(ctx, func(cc context.Context) error {
			v, err := k.GwVotes.Get(cc, id)
			switch {
			case errors.Is(err, collections.ErrNotFound):
				return k.GwVotesByVintage.Remove(cc, key) // orphaned: nothing to delete
			case err != nil:
				return err
			case v.Vintage != key.K1() || v.Validator != key.K2():
				// A stale entry: the vote is indexed (and swept) under its own.
				return k.GwVotesByVintage.Remove(cc, key)
			}
			if err := k.removeVote(cc, v); err != nil {
				return err
			}
			k.voteEvent(cc, "retired", v)
			return nil
		})
		if err == nil {
			continue
		}
		k.failure(ctx, "dead_votes", key.K2(), errorsmod.Wrapf(err, "vote %d parked", id))
		if err := k.guarded(ctx, func(cc context.Context) error { return k.GwVotesByVintage.Remove(cc, key) }); err != nil {
			k.failure(ctx, "dead_votes", key.K2(), errorsmod.Wrapf(err, "vote %d", id))
		}
	}
}

// ---- withdrawal of a retired note -------------------------------------------

// checkWithdrawRetired refuses a withdrawal of d derth of valoper's retired
// vintage m that withdrawRetired would: its retirement not booked yet,
// more derth than remains, a Groundworks split (a withdrawal's output does
// not vote). Returns the record and the ERTH value it splits off.
func (k Keeper) checkWithdrawRetired(ctx context.Context, m *types.MsgUndelegate, vintage uint32) (types.RetiredVintage, math.Int, error) {
	if len(m.GroundworksSplit) > 0 || !isZeroBytes(m.Stake.VoteTag) {
		return types.RetiredVintage{}, math.Int{}, types.ErrRetiredVintage.Wrap("a withdrawal's output does not vote")
	}
	rv, err := k.RetiredVintages.Get(ctx, collections.Join(m.Validator, vintage))
	if errors.Is(err, collections.ErrNotFound) {
		return rv, math.Int{}, types.ErrRetiredVintage.Wrapf("vintage %d at %s has nothing to withdraw (its retirement is booked at the next epoch end, or it was withdrawn whole)", vintage, m.Validator)
	} else if err != nil {
		return rv, math.Int{}, err
	}
	d := math.NewIntFromUint64(m.Amount)
	if d.GT(rv.RemainingSupply) {
		return rv, math.Int{}, errorsmod.Wrapf(types.ErrAmount, "vintage %d at %s has %s derth left to withdraw", vintage, m.Validator, rv.RemainingSupply)
	}
	w := rv.RemainingValue
	if !d.Equal(rv.RemainingSupply) {
		w = d.Mul(rv.RemainingValue).Quo(rv.RemainingSupply)
	}
	// At most one note's worth, as an undelegation (its response's value is
	// a u64); a larger share withdraws in parts.
	if w.IsPositive() {
		if err := fitsNote(w); err != nil {
			return rv, math.Int{}, err
		}
	}
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return rv, math.Int{}, err
	}
	return rv, w, nil
}

// withdrawRetired splits the withdrawn derth's share w off the vintage's
// held payout into a payout to the msg's pc (queued under the same record:
// paid at its maturity, or by the next block's sweep when it has matured),
// after the proof's spend. Returns the new payout's id (meaningless when w
// is 0: nothing is queued).
func (k Keeper) withdrawRetired(ctx context.Context, m *types.MsgUndelegate, vintage uint32) (math.Int, uint64, error) {
	rv, w, err := k.checkWithdrawRetired(ctx, m, vintage)
	if err != nil {
		return math.Int{}, 0, err
	}
	if _, err := k.applyStake(ctx, m); err != nil {
		return math.Int{}, 0, err
	}
	d := math.NewIntFromUint64(m.Amount)
	var id uint64
	if w.IsPositive() {
		held, err := k.UnbondPayouts.Get(ctx, rv.PayoutId)
		if err != nil {
			return math.Int{}, 0, errorsmod.Wrapf(types.ErrInvariant, "vintage %d at %s: held payout %d: %v", vintage, m.Validator, rv.PayoutId, err)
		}
		if held.Value.LT(w) || !held.Value.Equal(rv.RemainingValue) {
			return math.Int{}, 0, types.ErrInvariant.Wrapf("vintage %d at %s: held payout %s, remaining value %s", vintage, m.Validator, held.Value, rv.RemainingValue)
		}
		held.Value = held.Value.Sub(w)
		if held.Value.IsZero() {
			if err := k.UnbondPayouts.Remove(ctx, held.Id); err != nil {
				return math.Int{}, 0, err
			}
		} else if err := k.UnbondPayouts.Set(ctx, held.Id, held); err != nil {
			return math.Int{}, 0, err
		}
		if id, err = k.queuePayout(ctx, held.Validator, held.Epoch, w, m.Pc, m.Ciphertext); err != nil {
			return math.Int{}, 0, err
		}
		r, err := k.UnbondRecords.Get(ctx, collections.Join(held.Validator, held.Epoch))
		if err != nil {
			return math.Int{}, 0, err
		}
		// A matured record is marked for the next sweep; an unmatured one
		// is marked when it matures (markMatured).
		if r.Status == types.UNBOND_STATUS_MATURED {
			if err := k.markMatured(ctx, r); err != nil {
				return math.Int{}, 0, err
			}
		}
	}
	rv.RemainingSupply = rv.RemainingSupply.Sub(d)
	rv.RemainingValue = rv.RemainingValue.Sub(w)
	key := collections.Join(m.Validator, vintage)
	if rv.RemainingSupply.IsZero() {
		if err := k.RetiredVintages.Remove(ctx, key); err != nil {
			return math.Int{}, 0, err
		}
	} else if err := k.RetiredVintages.Set(ctx, key, rv); err != nil {
		return math.Int{}, 0, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeRetiredWithdrawal,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyVintage, strconv.FormatUint(uint64(vintage), 10)),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
		sdk.NewAttribute(types.AttributeKeyValue, w.String()),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(rv.Epoch, 10)),
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(id, 10)),
		sdk.NewAttribute(types.AttributeKeyRemainingSupply, rv.RemainingSupply.String()),
		sdk.NewAttribute(types.AttributeKeyRemainingValue, rv.RemainingValue.String()),
	))
	return w, id, nil
}

// ---- check-in -----------------------------------------------------------------

// checkCheckIn refuses a check-in its handler would: a validator that is
// not one, a credit vintage other than the current month (or the previous
// one in a month's first VintageGraceSeconds), a from_vintage that is not
// live or not older than the credit vintage, more derth than the vintage
// holds.
func (k Keeper) checkCheckIn(ctx context.Context, m *types.MsgCheckIn) error {
	if _, err := k.valAddr(m.Validator); err != nil {
		return err
	}
	vc, err := k.vintageCtx(ctx)
	if err != nil {
		return err
	}
	to := vc.Credit(m.CreditVintage)
	if err := vc.CheckCredit(to); err != nil {
		return err
	}
	if err := checkLive(m.FromVintage, vc.Current); err != nil {
		return err
	}
	if m.FromVintage >= to {
		return types.ErrVintage.Wrapf("vintage %d is not older than %d: nothing to check in", m.FromVintage, to)
	}
	s, err := k.VintageSupply(ctx, m.Validator, m.FromVintage)
	if err != nil {
		return err
	}
	if math.NewIntFromUint64(m.Amount).GT(s) {
		return errorsmod.Wrapf(types.ErrAmount, "vintage %d at %s holds %s derth", m.FromVintage, m.Validator, s)
	}
	return nil
}

// CheckIn moves amount of derth from from_vintage into the current vintage
// at the validator: the proof spends the old notes (change and a kept
// label's exposure stay in from_vintage) and merges amount into the
// current vintage's note, unlabelled. No value moves.
func (k msgServer) CheckIn(goCtx context.Context, m *types.MsgCheckIn) (*types.MsgCheckInResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	if err := k.checkCheckIn(ctx, m); err != nil {
		return nil, err
	}
	vc, err := k.vintageCtx(ctx)
	if err != nil {
		return nil, err
	}
	cur := vc.Credit(m.CreditVintage)
	pos, err := k.applyStake(ctx, m)
	if err != nil {
		return nil, err
	}
	amt := math.NewIntFromUint64(m.Amount)
	if err := k.addVintageSupply(ctx, m.Validator, m.FromVintage, amt.Neg()); err != nil {
		return nil, err
	}
	if err := k.addVintageSupply(ctx, m.Validator, cur, amt); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeCheckIn,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyFromVintage, strconv.FormatUint(uint64(m.FromVintage), 10)),
		sdk.NewAttribute(types.AttributeKeyToVintage, strconv.FormatUint(uint64(cur), 10)),
		sdk.NewAttribute(types.AttributeKeyDerth, amt.String()),
	))
	var p uint64
	if len(pos) > 0 {
		p = pos[len(pos)-1]
	}
	return &types.MsgCheckInResponse{Vintage: cur, Position: p}, nil
}
