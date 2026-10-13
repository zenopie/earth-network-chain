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
// the 1st of month m + 13 vintage m retires: in the first block at or after
// that time, the monthly sweep (retireVintages, BeginBlock; bounded per
// block, catching up every month a halt skipped) takes each book's due
// vintages out of its supply at the live rate, V = floor(S_{v,m} x B_v /
// S_v), and books V as one private undelegation of the current epoch held
// for the vintage (an UnbondPayout with no pc): it leaves x/staking with
// the epoch's other undelegations, slashes cut it as they cut any (the
// pending record's target, the SDK entry), and it earns nothing more. The
// vintage's Groundworks totals are dropped at the same time.
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
	return types.VintageCtx{Base: base, Current: k.currentVintage(ctx)}, nil
}

// baseVintage is the vintage whose denom is the unsuffixed derth/<valoper>.
// Unset only before InitGenesis / the v1.3.0 upgrade: the current month.
func (k Keeper) baseVintage(ctx context.Context) (uint32, error) {
	b, err := k.BaseVintage.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return k.currentVintage(ctx), nil
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

// retireVintages is the BeginBlock retirement sweep. Once a month (the
// first block of a new UTC month, or after a halt the first block after
// it) it walks every book, VintageRetireLimit a block from a persistent
// cursor, retiring each book's vintages older than the month's oldest live
// one (retireBook). A pass that failed a book runs again a day later. Never
// fails.
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
		k.failure(ctx, "vintage_sweep", sweep.Cursor, err)
		return
	}
	failed := false
	for _, v := range vals {
		if err := k.guarded(ctx, func(cc context.Context) error { return k.retireBook(cc, v, cur) }); err != nil {
			k.failure(ctx, "retire_vintage", v, err)
			failed = true
		}
	}
	if failed && sweep.RetryAt == 0 {
		sweep.RetryAt = now + types.VintageRetryDelay
	}
	if len(vals) < types.VintageRetireLimit {
		sweep.Active, sweep.Cursor = false, ""
		if !failed {
			sweep.RetryAt = 0
		}
	} else {
		sweep.Cursor = vals[len(vals)-1]
	}
	if err := k.VintageSweep.Set(ctx, sweep); err != nil {
		k.failure(ctx, "vintage_sweep", "", err)
	}
}

// retireBook retires every vintage of valoper's book that has retired by
// month cur (oldest first: all a halt skipped) and drops their Groundworks
// totals; a voter that changed is re-filed at the end of the block
// (GwResync), after x/allocation's BeginBlock has settled the stream.
func (k Keeper) retireBook(ctx context.Context, valoper string, cur uint32) error {
	oldest := types.OldestLiveVintage(cur)
	rng := collections.NewPrefixedPairRange[string, uint32](valoper).EndExclusive(oldest)
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
	for _, d := range dues {
		if err := k.retireVintage(ctx, valoper, d.m, d.s); err != nil {
			return err
		}
	}
	dropped, err := k.dropStaleTotals(ctx, valoper, cur)
	if err != nil {
		return err
	}
	if dropped || len(dues) > 0 {
		if _, err := k.GwEpoch.Get(ctx, valoper); err == nil || dropped {
			return k.GwResync.Set(ctx, valoper)
		}
	}
	return nil
}

// retireVintage takes vintage m's supply s out of valoper's book at the
// live rate and books its value V = floor(s x B / S) as a held
// undelegation of the current epoch (the record's requested, target and
// outstanding, the book's pending undelegation; an UnbondPayout with no
// pc), recording the RetiredVintage the vintage's notes withdraw from.
func (k Keeper) retireVintage(ctx context.Context, valoper string, m uint32, s math.Int) error {
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
	epoch, err := k.Epoch.Get(ctx)
	if err != nil {
		return err
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
		Validator: valoper, Vintage: m, Supply: s, Value: v, Epoch: epoch.Number,
		RemainingSupply: s, RemainingValue: v, RetiredAt: sdk.UnwrapSDKContext(ctx).BlockTime().Unix(),
	}
	if v.IsPositive() {
		key := collections.Join(valoper, epoch.Number)
		r, err := k.UnbondRecords.Get(ctx, key)
		if errors.Is(err, collections.ErrNotFound) {
			r = types.UnbondRecord{
				Validator: valoper, Epoch: epoch.Number, Status: types.UNBOND_STATUS_PENDING,
				Requested: math.ZeroInt(), Target: math.ZeroInt(), Undelegated: math.ZeroInt(),
				Payout: math.ZeroInt(), Outstanding: math.ZeroInt(), Paid: math.ZeroInt(),
			}
		} else if err != nil {
			return err
		} else if r.Status != types.UNBOND_STATUS_PENDING {
			return types.ErrNotMatured.Wrapf("%s/%d is already %s", valoper, epoch.Number, r.Status)
		}
		r.Requested = r.Requested.Add(v)
		r.Target = r.Target.Add(v)
		r.Outstanding = r.Outstanding.Add(v)
		if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
			return err
		}
		if err := k.PendingRecords.Set(ctx, key); err != nil {
			return err
		}
		vs.PendingUndelegation = vs.PendingUndelegation.Add(v)
		id, err := k.UnbondPayoutSeq.Next(ctx)
		if err != nil {
			return err
		}
		// Held: queued nowhere, so the sweep never pays it; withdrawals
		// split it (withdrawRetired).
		if err := k.UnbondPayouts.Set(ctx, id, types.UnbondPayout{Id: id, Validator: valoper, Epoch: epoch.Number, Value: v}); err != nil {
			return err
		}
		rv.PayoutId = id
	}
	if err := k.Validators.Set(ctx, valoper, vs); err != nil {
		return err
	}
	if err := k.VintageSupplies.Remove(ctx, collections.Join(valoper, m)); err != nil {
		return err
	}
	if err := k.RetiredVintages.Set(ctx, collections.Join(valoper, m), rv); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeVintageRetired,
		sdk.NewAttribute(types.AttributeKeyValidator, valoper),
		sdk.NewAttribute(types.AttributeKeyVintage, strconv.FormatUint(uint64(m), 10)),
		sdk.NewAttribute(types.AttributeKeySupply, s.String()),
		sdk.NewAttribute(types.AttributeKeyValue, v.String()),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(epoch.Number, 10)),
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(rv.PayoutId, 10)),
	))
	return nil
}

// resyncRetired re-files, in EndBlock, the voters whose totals a
// retirement dropped this block. Never fails.
func (k Keeper) resyncRetired(ctx context.Context) {
	var vals []string
	_ = k.GwResync.Walk(ctx, nil, func(v string) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
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
// withdrawal deletes its own sooner. Never fails.
func (k Keeper) sweepDeadVotes(ctx context.Context) {
	oldest := types.OldestLiveVintage(k.currentVintage(ctx))
	rng := new(collections.Range[collections.Triple[uint32, string, uint64]]).
		EndExclusive(collections.TriplePrefix[uint32, string, uint64](oldest))
	var dead []uint64
	if err := k.GwVotesByVintage.Walk(ctx, rng, func(key collections.Triple[uint32, string, uint64]) (bool, error) {
		dead = append(dead, key.K3())
		return len(dead) >= types.DeadVoteSweepLimit, nil
	}); err != nil {
		k.failure(ctx, "dead_votes", "", err)
		return
	}
	for _, id := range dead {
		err := k.guarded(ctx, func(cc context.Context) error {
			v, err := k.GwVotes.Get(cc, id)
			if errors.Is(err, collections.ErrNotFound) {
				return nil
			} else if err != nil {
				return err
			}
			if err := k.removeVote(cc, v); err != nil {
				return err
			}
			k.voteEvent(cc, "retired", v)
			return nil
		})
		if err != nil {
			k.failure(ctx, "dead_votes", "", err)
			return
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
		return rv, math.Int{}, types.ErrRetiredVintage.Wrapf("vintage %d at %s has nothing to withdraw (its retirement is not booked yet, or it was withdrawn whole)", vintage, m.Validator)
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
// not one, a from_vintage that is not live or not older than the current
// month, more derth than the vintage holds.
func (k Keeper) checkCheckIn(ctx context.Context, m *types.MsgCheckIn) error {
	if _, err := k.valAddr(m.Validator); err != nil {
		return err
	}
	cur := k.currentVintage(ctx)
	if err := checkLive(m.FromVintage, cur); err != nil {
		return err
	}
	if m.FromVintage == cur {
		return types.ErrVintage.Wrapf("vintage %d is the current one: nothing to check in", cur)
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
	cur := k.currentVintage(ctx)
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
