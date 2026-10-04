package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Note-enforced slash debt (ORCHARD_DESIGN.md section 20).
//
// A private redelegation ("move") from src to dst credits derth/dst to its
// owner's note LABELLED with the move: (move key = the credit nullifier,
// move_time, exposed = the credited derth), hidden in the note's commitment.
// The bonded part of the value moves with x/staking's primitives (Unbond at
// src, Delegate at dst, a redelegation entry the module records itself), so
// there is no transitive lock and no max_entries, and x/staking still
// slashes the entry when src is punished for an infraction before the move:
// it unbonds slash_fraction x the entry's shares from the module's
// delegation at dst and burns them.
//
// The module covers that burn so dst's rate does not move: the derth the
// burnt value backed is taken off dst's supply (ValidatorState.slash_debt),
// and the moves in the slashed entries owe it, pro rata to their shares, as
// rows of the slash debt tree (debt_tree.go): what each move's exposure is
// still worth. The exposure never leaves its note while the move's entry can
// be slashed (the circuit keeps the label on the note and moves only its
// unexposed value), so every note that owes is in dst's book. Once the
// entry has matured (the label window: the longest unbonding_time seen
// plus MoveTimeSlackSeconds after move_time) the next stake proof spending
// the note clears the label at the row's retained value (or the whole
// exposure for a move never slashed): from then on the note holds exactly
// what it is worth, and the supply already left the rest out. Votes count a
// labelled note at its current value. Nothing about which note owes, or how
// much a note holds, becomes public.
//
// Which moves a slash reached: x/staking calls BeforeValidatorModified(src)
// as a slash begins (openSlashWatch: the module's shares at each dst), then
// unbonds the module's delegation at dst once per slashed entry of the
// module's (src, dst) redelegation (BeforeDelegationSharesModified:
// countSlashUnbond). The slashed entries are the last that many unmatured
// entries, in creation-height order (x/staking slashes an entry iff it was
// created at or after the infraction and has not matured), and the burnt
// shares are the fall of the module's shares at dst (finishSlashWatch, at
// the next slash or this module's BeginBlocker, right after x/slashing's and
// x/evidence's: no tx runs in between).

// labelWindow is how long after its move_time a label lasts: the longest
// x/staking unbonding_time seen (the entry's maturity, from any block time up
// to MoveTimeSlackSeconds after move_time), plus that slack.
func (k Keeper) labelWindow(ctx context.Context) (uint64, error) {
	max, err := k.MaxUnbonding.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return 0, err
	}
	if cur := unbondingSeconds(ut); cur > max {
		max = cur
	}
	return max + types.MoveTimeSlackSeconds, nil
}

func unbondingSeconds(d time.Duration) uint64 {
	s := uint64(d / time.Second)
	if d%time.Second != 0 {
		s++
	}
	return s
}

// noteMaxUnbonding records the current unbonding_time if it is the longest
// seen (EndBlocker, InitGenesis): a later cut by governance does not shorten
// the window of labels made before it.
func (k Keeper) noteMaxUnbonding(ctx context.Context) error {
	max, err := k.MaxUnbonding.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return err
	}
	if cur := unbondingSeconds(ut); cur > max {
		return k.MaxUnbonding.Set(ctx, cur)
	}
	return nil
}

// ClearBefore is the latest clear_before a stake proof may name now: the
// block time less the label window (0 before the chain is that old).
func (k Keeper) ClearBefore(ctx context.Context) (uint64, error) {
	w, err := k.labelWindow(ctx)
	if err != nil {
		return 0, err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	if now < 0 || uint64(now) <= w {
		return 0, nil
	}
	return uint64(now) - w, nil
}

// checkStakeClear: a proof that may clear a label (clear_before > 0) names a
// clear_before the label window allows now, and reads the current debt
// root. The circuit clears only a label with move_time < clear_before.
func (k Keeper) checkStakeClear(ctx context.Context, p *types.StakeProof) error {
	if p.ClearBefore == 0 {
		return nil
	}
	cb, err := k.ClearBefore(ctx)
	if err != nil {
		return err
	}
	if p.ClearBefore > cb {
		return types.ErrStakeTree.Wrapf("clear_before %d is after %d: labels newer than that may still be slashed", p.ClearBefore, cb)
	}
	return k.checkDebtRoot(ctx, p.DebtRoot)
}

// checkMoveTime refuses a move_time the block time is not within
// [move_time, move_time + MoveTimeSlackSeconds] of.
func checkMoveTime(ctx context.Context, moveTime uint64) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	if now < 0 || moveTime > uint64(now) || uint64(now)-moveTime > types.MoveTimeSlackSeconds {
		return errorsmod.Wrapf(types.ErrRedelegation, "move_time %d is not within %ds before the block time %d (name a recent block's time)",
			moveTime, types.MoveTimeSlackSeconds, now)
	}
	return nil
}

func entryID(src, dst string) string { return src + "/" + dst }

// moveBonded moves shares worth `bonded` of the module's delegation from src
// to dst at once, as x/staking's BeginRedelegation does but without its
// transitive and max_entries refusals: Unbond at src, Delegate at dst, and
// the redelegation entry recorded (one per (src, dst, block): moves in one
// block share it), so a slash of src for an infraction before the move still
// reaches it. Returns the shares created at dst and the entry's height and
// completion (both 0 when src is unbonded: no entry, nothing to slash).
func (k Keeper) moveBonded(ctx sdk.Context, src, dst sdk.ValAddress, bonded math.Int) (math.LegacyDec, int64, int64, error) {
	srcVal, err := k.staking.GetValidator(ctx, src)
	if err != nil {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrapf(types.ErrRedelegation, "source: %v", err)
	}
	dstVal, err := k.staking.GetValidator(ctx, dst)
	if err != nil {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrapf(types.ErrRedelegation, "destination: %v", err)
	}
	shares, err := k.staking.ValidateUnbondAmount(ctx, k.modAddr, src, bonded)
	if err != nil {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrapf(types.ErrRedelegation, "%s from the source: %v", bonded, err)
	}
	returned, err := k.staking.Unbond(ctx, k.modAddr, src, shares)
	if err != nil {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrapf(types.ErrRedelegation, "unbond: %v", err)
	}
	if !returned.IsPositive() {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrap(types.ErrRedelegation, "the source returns nothing for that value")
	}
	created, err := k.staking.Delegate(ctx, k.modAddr, returned, srcVal.GetStatus(), dstVal, false)
	if err != nil {
		return math.LegacyDec{}, 0, 0, errorsmod.Wrapf(types.ErrRedelegation, "delegate: %v", err)
	}
	// x/staking's getBeginInfo, after the unbond (a source that just lost
	// its last bonded tokens may be unbonding now).
	if srcVal, err = k.staking.GetValidator(ctx, src); err != nil {
		return math.LegacyDec{}, 0, 0, err
	}
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return math.LegacyDec{}, 0, 0, err
	}
	var completion time.Time
	var height int64
	switch {
	case srcVal.IsBonded():
		completion, height = ctx.BlockTime().Add(ut), ctx.BlockHeight()
	case srcVal.IsUnbonding():
		completion, height = srcVal.UnbondingTime, srcVal.UnbondingHeight
	default: // unbonded: nothing can be slashed, no entry
		return created, 0, 0, nil
	}
	h, c, err := k.recordEntry(ctx, src, dst, height, completion, returned, shares, created)
	return created, h, c, err
}

// recordEntry adds the move to the module's (src, dst) redelegation: into the
// entry of the same creation height if there is one; past
// MaxEntryHeightsPerPair entries, into the latest, which keeps its height
// and completion; else as a new entry, queued for completion. Returns the
// entry's height and completion (ns), which the move records.
//
// Joining the latest entry keeps the books sound: every move in an entry
// was made at or after its creation height, so a slash of it charges no
// move made before the infraction, and the entry matures no later than any
// of its moves' labels clear, so no slash reaches a move whose label has
// cleared. What it gives up, only past the cap: an infraction between the
// entry's height and the joining move's is charged to the source's stake
// instead of the move.
func (k Keeper) recordEntry(ctx sdk.Context, src, dst sdk.ValAddress, height int64, completion time.Time,
	balance math.Int, sharesSrc, sharesDst math.LegacyDec,
) (int64, int64, error) {
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, src, dst)
	if errors.Is(err, stakingtypes.ErrNoRedelegation) {
		red, err = k.staking.SetRedelegationEntry(ctx, k.modAddr, src, dst, height, completion, balance, sharesSrc, sharesDst)
		if err != nil {
			return 0, 0, err
		}
		return height, completion.UnixNano(), k.staking.InsertRedelegationQueue(ctx, red, completion)
	} else if err != nil {
		return 0, 0, err
	}
	n := len(red.Entries)
	if n > 0 && (n >= types.MaxEntryHeightsPerPair ||
		red.Entries[n-1].CreationHeight == height && red.Entries[n-1].CompletionTime.Equal(completion)) {
		e := &red.Entries[n-1]
		e.InitialBalance = e.InitialBalance.Add(balance)
		e.SharesDst = e.SharesDst.Add(sharesDst)
		return e.CreationHeight, e.CompletionTime.UnixNano(), k.staking.SetRedelegation(ctx, red)
	}
	if red, err = k.staking.SetRedelegationEntry(ctx, k.modAddr, src, dst, height, completion, balance, sharesSrc, sharesDst); err != nil {
		return 0, 0, err
	}
	return height, completion.UnixNano(), k.staking.InsertRedelegationQueue(ctx, red, completion)
}

func (k Keeper) valString(v sdk.ValAddress) string {
	s, _ := k.staking.ValidatorAddressCodec().BytesToString(v)
	return s
}

// putMove stores a move and its indexes.
func (k Keeper) putMove(ctx context.Context, mv types.Move) error {
	if err := k.Moves.Set(ctx, mv.Key, mv); err != nil {
		return err
	}
	if err := k.MovesByEntry.Set(ctx, collections.Join3(entryID(mv.SrcValidator, mv.DstValidator), mv.EntryHeight, mv.Key)); err != nil {
		return err
	}
	return k.MovesByCompletion.Set(ctx, collections.Join(mv.Completion, mv.Key))
}

// pruneMoves forgets moves whose entry has matured (x/staking no longer
// slashes it): at most limit a block. A slashed move keeps its debt row.
func (k Keeper) pruneMoves(ctx context.Context, limit int) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	var done []collections.Pair[int64, []byte]
	if err := k.MovesByCompletion.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if key.K1() > now || len(done) >= limit {
			return true, nil
		}
		done = append(done, key)
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range done {
		mv, err := k.Moves.Get(ctx, key.K2())
		if err != nil {
			return err
		}
		if err := k.MovesByEntry.Remove(ctx, collections.Join3(entryID(mv.SrcValidator, mv.DstValidator), mv.EntryHeight, mv.Key)); err != nil {
			return err
		}
		if err := k.MovesByCompletion.Remove(ctx, key); err != nil {
			return err
		}
		if err := k.Moves.Remove(ctx, key.K2()); err != nil {
			return err
		}
	}
	return nil
}

// ---- which moves a slash reached ---------------------------------------------

// openSlashWatch starts watching a slash of src (x/staking's
// BeforeValidatorModified, outside txs): the module's shares at every
// destination of its redelegations from src. Never fails the slash.
func (k Keeper) openSlashWatch(ctx context.Context, src sdk.ValAddress, reds []stakingtypes.Redelegation) {
	k.finishSlashWatch(ctx)
	srcoper := k.valString(src)
	if err := k.guarded(ctx, func(cc context.Context) error {
		if err := k.WatchSrc.Set(cc, srcoper); err != nil {
			return err
		}
		// The source is re-weighed at the end of the block, like every
		// destination (prepareRedelegationSlash), even when x/staking takes
		// the whole slash from the entries and never calls
		// BeforeValidatorSlashed for it (audit 7).
		if err := k.SlashedValidators.Set(cc, srcoper); err != nil {
			return err
		}
		mod := k.modString(cc)
		for _, r := range reds {
			if r.DelegatorAddress != mod {
				continue
			}
			dst, err := k.valAddr(r.ValidatorDstAddress)
			if err != nil {
				return err
			}
			shares := math.LegacyZeroDec()
			if del, err := k.staking.GetDelegation(cc, k.modAddr, dst); err == nil {
				shares = del.Shares
			} else if !errors.Is(err, stakingtypes.ErrNoDelegation) {
				return err
			}
			if err := k.WatchShares.Set(cc, r.ValidatorDstAddress, shares); err != nil {
				return err
			}
			if err := k.WatchCalls.Set(cc, r.ValidatorDstAddress, 0); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		k.failure(ctx, "slash_watch", srcoper, err)
	}
}

// countSlashUnbond counts x/staking's unbonding of the module's delegation at
// val while a slash is watched: one per slashed entry of the module's
// redelegation from the source to val.
func (k Keeper) countSlashUnbond(ctx context.Context, del sdk.AccAddress, val sdk.ValAddress) {
	if !del.Equals(k.modAddr) || len(sdk.UnwrapSDKContext(ctx).TxBytes()) != 0 {
		return
	}
	if has, err := k.WatchSrc.Has(ctx); err != nil || !has {
		return
	}
	dst := k.valString(val)
	n, err := k.WatchCalls.Get(ctx, dst)
	if err != nil {
		return // not a destination of the source's redelegations
	}
	if err := k.WatchCalls.Set(ctx, dst, n+1); err != nil {
		k.failure(ctx, "slash_watch", dst, err)
	}
}

// finishSlashWatch settles the watched slash, if any: per destination, the
// burnt shares become slash debt owed by the moves of the slashed entries.
// Never fails; the watch is cleared.
func (k Keeper) finishSlashWatch(ctx context.Context) {
	src, err := k.WatchSrc.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return
	}
	type dstWatch struct {
		dst    string
		before math.LegacyDec
		calls  uint64
	}
	var dsts []dstWatch
	_ = k.WatchShares.Walk(ctx, nil, func(dst string, before math.LegacyDec) (bool, error) {
		calls, _ := k.WatchCalls.Get(ctx, dst)
		dsts = append(dsts, dstWatch{dst, before, calls})
		return false, nil
	})
	for _, d := range dsts {
		if d.calls == 0 {
			continue
		}
		if err := k.guarded(ctx, func(cc context.Context) error {
			return k.settleSlash(cc, src, d.dst, d.before, d.calls)
		}); err != nil {
			k.failure(ctx, "slash_debt", d.dst, err)
		}
	}
	if err := k.guarded(ctx, func(cc context.Context) error {
		for _, d := range dsts {
			if err := k.WatchShares.Remove(cc, d.dst); err != nil {
				return err
			}
			if err := k.WatchCalls.Remove(cc, d.dst); err != nil {
				return err
			}
		}
		return k.WatchSrc.Remove(cc)
	}); err != nil {
		k.failure(ctx, "slash_watch", src, err)
	}
}

// settleSlash books the slash of src's redelegations into dst: x/staking
// unbonded `calls` entries' slash shares from the module's delegation at dst
// (its shares fell from before) and burnt them. That value's derth comes off
// dst's supply, so dst's rate is what it was before the burn, and the moves
// of the slashed entries owe it pro rata to their shares (their debt rows).
func (k Keeper) settleSlash(ctx context.Context, src, dstoper string, before math.LegacyDec, calls uint64) error {
	srcAddr, err := k.valAddr(src)
	if err != nil {
		return err
	}
	dst, err := k.valAddr(dstoper)
	if err != nil {
		return err
	}
	now := math.LegacyZeroDec()
	if del, err := k.staking.GetDelegation(ctx, k.modAddr, dst); err == nil {
		now = del.Shares
	} else if !errors.Is(err, stakingtypes.ErrNoDelegation) {
		return err
	}
	burnt := before.Sub(now)
	if !burnt.IsPositive() {
		return nil
	}
	// The slashed entries: the last `calls` unmatured ones (x/staking takes
	// an entry iff created at or after the infraction and not matured).
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, srcAddr, dst)
	if err != nil {
		return err
	}
	blockTime := sdk.UnwrapSDKContext(ctx).BlockTime()
	var open []stakingtypes.RedelegationEntry
	for _, e := range red.Entries {
		if !e.IsMature(blockTime) {
			open = append(open, e)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].CreationHeight < open[j].CreationHeight })
	if uint64(len(open)) > calls {
		open = open[uint64(len(open))-calls:]
	}
	id := entryID(src, dstoper)
	var moves []types.Move
	weight := math.LegacyZeroDec()
	for _, e := range open {
		rng := collections.NewSuperPrefixedTripleRange[string, int64, []byte](id, e.CreationHeight)
		if err := k.MovesByEntry.Walk(ctx, rng, func(key collections.Triple[string, int64, []byte]) (bool, error) {
			mv, err := k.Moves.Get(ctx, key.K3())
			if err != nil {
				return true, err
			}
			moves = append(moves, mv)
			weight = weight.Add(mv.Shares)
			return false, nil
		}); err != nil {
			return err
		}
	}
	if !weight.IsPositive() {
		return errorsmod.Wrapf(types.ErrRedelegation, "no move owns the slashed %s -> %s entries: the book absorbs %s shares", src, dstoper, burnt)
	}
	// The burnt shares' value now, and the derth it backed at dst's rate
	// before the burn: dst's rate is (B + v) / S before and B / (S - debt)
	// after, equal for debt = v x S / (B + v).
	v, err := k.staking.GetValidator(ctx, dst)
	if err != nil {
		return err
	}
	value := v.TokensFromShares(burnt).TruncateInt()
	b, s, err := k.Backing(ctx, dstoper)
	if err != nil {
		return err
	}
	if !s.IsPositive() || !value.IsPositive() {
		return nil
	}
	total := value.Mul(s).Quo(b.Add(value))
	booked := math.ZeroInt()
	for _, mv := range moves {
		d := math.LegacyNewDecFromInt(total).Mul(mv.Shares).Quo(weight).TruncateInt()
		if d.GT(mv.Retained) {
			d = mv.Retained
		}
		if !d.IsPositive() {
			continue
		}
		mv.Retained = mv.Retained.Sub(d)
		booked = booked.Add(d)
		if err := k.Moves.Set(ctx, mv.Key, mv); err != nil {
			return err
		}
		if err := k.setDebtRow(ctx, mv.Key, mv.Retained.Uint64()); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeMoveSlashed,
			sdk.NewAttribute(types.AttributeKeyMoveKey, hex.EncodeToString(mv.Key)),
			sdk.NewAttribute(types.AttributeKeySrcValidator, src),
			sdk.NewAttribute(types.AttributeKeyDstValidator, dstoper),
			sdk.NewAttribute(types.AttributeKeyDebt, d.String()),
			sdk.NewAttribute(types.AttributeKeyRetained, mv.Retained.String()),
		))
	}
	vs, err := k.ValidatorState(ctx, dstoper)
	if err != nil {
		return err
	}
	if err := k.checkpointSupply(ctx, &vs); err != nil {
		return err
	}
	if booked.GT(vs.DerthSupply) {
		booked = vs.DerthSupply
	}
	vs.DerthSupply = vs.DerthSupply.Sub(booked)
	vs.SlashDebt = vs.SlashDebt.Add(booked)
	if err := k.Validators.Set(ctx, dstoper, vs); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSlashDebt,
		sdk.NewAttribute(types.AttributeKeySrcValidator, src),
		sdk.NewAttribute(types.AttributeKeyDstValidator, dstoper),
		sdk.NewAttribute(types.AttributeKeyValue, value.String()),
		sdk.NewAttribute(types.AttributeKeyDebt, booked.String()),
		sdk.NewAttribute(types.AttributeKeyEntries, strconv.FormatUint(calls, 10)),
	))
	return nil
}
