package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Note-enforced slash debt (ORCHARD_DESIGN.md 8.7).
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
// unbonds the module's delegation at dst once per entry of the module's
// (src, dst) redelegation it slashes (BeforeDelegationSharesModified:
// countSlashUnbond). No hook names the slash's fraction or infraction height,
// so finishSlashWatch (at the next slash, or this module's BeginBlocker,
// right after x/slashing's and x/evidence's: no tx runs in between) replays
// x/staking's own rule (SlashRedelegation) on the pair's entries for each
// fraction x/staking is ever called with (x/slashing's downtime and
// x/evidence's double sign, read from x/slashing's params in the same block)
// and each infraction height an entry boundary allows: the one replay that
// makes exactly the counted unbonds and burns exactly the fall of the
// module's shares is the slash (slashedEntries). An entry x/staking skips
// (its slash truncates to nothing, or the delegation is gone) is skipped in
// the replay too, whatever slash fraction governance sets (audit 7, A7-L2).

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

// checkStakeClear: every stake proof names the label window's current
// clear_before and the current debt root (audit 7, B L-1 / A7-L3), whether or
// not it clears a label, so a proof that clears one looks like every other:
// clear_before within ClearBeforeSlackSeconds below ClearBefore(now) (the
// proof was made against a recent block), and the debt root current. (Both
// are 0 only while the block time is less than the window, which no real
// chain sees.) The circuit clears only a label with move_time <
// clear_before, and checks nothing about either when it clears none.
func (k Keeper) checkStakeClear(ctx context.Context, p *types.StakeProof) error {
	cb, err := k.ClearBefore(ctx)
	if err != nil {
		return err
	}
	if cb == 0 {
		if p.ClearBefore != 0 {
			return types.ErrStakeTree.Wrapf("clear_before must be 0 while the block time is within the label window (it is %d)", p.ClearBefore)
		}
		return nil
	}
	lo := uint64(1)
	if cb > types.ClearBeforeSlackSeconds {
		lo = cb - types.ClearBeforeSlackSeconds
	}
	if p.ClearBefore < lo || p.ClearBefore > cb {
		return types.ErrStakeTree.Wrapf("clear_before %d is not within [%d, %d]: name the label window's current clear_before (Query/DebtTree)", p.ClearBefore, lo, cb)
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
// latest entry when it has the same creation height and completion (moves in
// one block, or from one unbonding source); else as a new entry, queued for
// completion, after merging the two oldest when the pair already holds
// MaxEntryHeightsPerPair (mergeOldEntries). Only if no two entries can be
// merged does the move join the latest entry, which keeps its height and
// completion. Returns the entry's height and completion (ns), which the move
// records.
func (k Keeper) recordEntry(ctx sdk.Context, src, dst sdk.ValAddress, height int64, completion time.Time,
	balance math.Int, sharesSrc, sharesDst math.LegacyDec,
) (int64, int64, error) {
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, src, dst)
	switch {
	case errors.Is(err, stakingtypes.ErrNoRedelegation):
	case err != nil:
		return 0, 0, err
	default:
		n := len(red.Entries)
		joinLast := n > 0 && red.Entries[n-1].CreationHeight == height && red.Entries[n-1].CompletionTime.Equal(completion)
		if !joinLast && countedEntries(red.Entries) >= types.MaxEntryHeightsPerPair {
			merged, err := k.mergeOldEntries(ctx, &red)
			if err != nil {
				return 0, 0, err
			}
			joinLast = !merged
		}
		if joinLast {
			e := &red.Entries[n-1]
			e.InitialBalance = e.InitialBalance.Add(balance)
			e.SharesDst = e.SharesDst.Add(sharesDst)
			return e.CreationHeight, e.CompletionTime.UnixNano(), k.staking.SetRedelegation(ctx, red)
		}
	}
	red, err = k.staking.SetRedelegationEntry(ctx, k.modAddr, src, dst, height, completion, balance, sharesSrc, sharesDst)
	if err != nil {
		return 0, 0, err
	}
	return height, completion.UnixNano(), k.staking.InsertRedelegationQueue(ctx, red, completion)
}

// countedEntries is how many entries count against MaxEntryHeightsPerPair:
// those of positive creation height (an entry at height 0 or below is a
// zero-height export's, never slashed on this chain, and matures away).
func countedEntries(es []stakingtypes.RedelegationEntry) int {
	n := 0
	for _, e := range es {
		if e.CreationHeight > 0 {
			n++
		}
	}
	return n
}

// mergeOldEntries merges two adjacent entries of red, the oldest pair whose
// moves (at most MaxMergeMoves together) can be re-filed, trying at most
// MergeTries pairs, and saves red. The merged entry takes the later
// creation height (a slash for an infraction between the two heights now
// charges the older entry's moves too: never less than x/staking would) and
// the earlier completion (it matures before any of its moves' labels can
// clear: no slash reaches a cleared label). Its moves' entry height and
// completion follow. Returns false, with nothing changed, when no pair
// qualifies.
func (k Keeper) mergeOldEntries(ctx context.Context, red *stakingtypes.Redelegation) (bool, error) {
	id := entryID(red.ValidatorSrcAddress, red.ValidatorDstAddress)
	var idx []int
	for i, e := range red.Entries {
		if e.CreationHeight > 0 {
			idx = append(idx, i)
		}
	}
	for a := 0; a+1 < len(idx) && a < types.MergeTries; a++ {
		i, j := idx[a], idx[a+1]
		ei, ej := red.Entries[i], red.Entries[j]
		mi, ok, err := k.entryMoves(ctx, id, ei, types.MaxMergeMoves)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		mj, ok, err := k.entryMoves(ctx, id, ej, types.MaxMergeMoves-len(mi))
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		m := ej
		if ei.CreationHeight > m.CreationHeight {
			m.CreationHeight = ei.CreationHeight
		}
		if ei.CompletionTime.Before(m.CompletionTime) {
			m.CompletionTime = ei.CompletionTime
		}
		m.InitialBalance = ei.InitialBalance.Add(ej.InitialBalance)
		m.SharesDst = ei.SharesDst.Add(ej.SharesDst)
		entries := make([]stakingtypes.RedelegationEntry, 0, len(red.Entries)-1)
		entries = append(entries, red.Entries[:i]...)
		entries = append(entries, red.Entries[i+1:j]...)
		entries = append(entries, m)
		entries = append(entries, red.Entries[j+1:]...)
		red.Entries = entries
		if err := k.staking.SetRedelegation(ctx, *red); err != nil {
			return false, err
		}
		// The merged-away entry's unbonding id indexes nothing now (its
		// queue slot finds the record and completes nothing, or completes
		// the merged entry if it matured).
		if err := k.staking.DeleteUnbondingIndex(ctx, ei.UnbondingId); err != nil {
			return false, err
		}
		for _, mv := range append(mi, mj...) {
			if err := k.removeMoveIndexes(ctx, mv); err != nil {
				return false, err
			}
			mv.EntryHeight, mv.Completion = m.CreationHeight, m.CompletionTime.UnixNano()
			if err := k.putMove(ctx, mv); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

// entryMoves is the moves of entry e of the pair id (its height and
// completion), or false when there are more than limit.
func (k Keeper) entryMoves(ctx context.Context, id string, e stakingtypes.RedelegationEntry, limit int) ([]types.Move, bool, error) {
	var out []types.Move
	over := false
	rng := collections.NewSuperPrefixedTripleRange[string, int64, []byte](id, e.CreationHeight)
	err := k.MovesByEntry.Walk(ctx, rng, func(key collections.Triple[string, int64, []byte]) (bool, error) {
		mv, err := k.Moves.Get(ctx, key.K3())
		if err != nil {
			return true, err
		}
		if mv.Completion != e.CompletionTime.UnixNano() {
			return false, nil // another entry at the same height (height <= 0 only)
		}
		if len(out) >= limit {
			over = true
			return true, nil
		}
		out = append(out, mv)
		return false, nil
	})
	return out, !over, err
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

// removeMoveIndexes removes a move's two indexes (not the move).
func (k Keeper) removeMoveIndexes(ctx context.Context, mv types.Move) error {
	if err := k.MovesByEntry.Remove(ctx, collections.Join3(entryID(mv.SrcValidator, mv.DstValidator), mv.EntryHeight, mv.Key)); err != nil {
		return err
	}
	return k.MovesByCompletion.Remove(ctx, collections.Join(mv.Completion, mv.Key))
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
// of the slashed entries owe it pro rata to the shares the slash took from
// each (their debt rows). Burnt shares no move owns (an entry at height 0 or
// below, whose moves a zero-height export dropped) stay with dst's book.
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
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, srcAddr, dst)
	if err != nil {
		return err
	}
	fractions, err := k.slashFractions(ctx)
	if err != nil {
		return err
	}
	hit, ok := slashedEntries(red.Entries, sdk.UnwrapSDKContext(ctx).BlockTime(), fractions, before, burnt, calls)
	if !ok {
		return errorsmod.Wrapf(types.ErrRedelegation, "no replay of x/staking's slash of the %s -> %s entries makes %d unbonds of %s shares: the book absorbs them",
			src, dstoper, calls, burnt)
	}
	id := entryID(src, dstoper)
	type owed struct {
		mv     types.Move
		weight math.LegacyDec
	}
	var moves []owed
	for _, h := range hit {
		ms, _, err := k.entryMoves(ctx, id, h.entry, int(^uint(0)>>1))
		if err != nil {
			return err
		}
		for _, mv := range ms {
			// The move's part of what the slash took from its entry.
			moves = append(moves, owed{mv, mv.Shares.Mul(h.shares).Quo(h.entry.SharesDst)})
		}
	}
	if len(moves) == 0 {
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
	for _, o := range moves {
		mv := o.mv
		d := math.LegacyNewDecFromInt(total).Mul(o.weight).Quo(burnt).TruncateInt()
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

// slashFractions is every fraction x/staking's Slash is called with: x/slashing's
// downtime fraction and x/evidence's double-sign one (x/evidence has no
// router, so nothing else slashes).
func (k Keeper) slashFractions(ctx context.Context) ([]math.LegacyDec, error) {
	ds, err := k.slashing.SlashFractionDoubleSign(ctx)
	if err != nil {
		return nil, err
	}
	dt, err := k.slashing.SlashFractionDowntime(ctx)
	if err != nil {
		return nil, err
	}
	if dt.Equal(ds) {
		return []math.LegacyDec{ds}, nil
	}
	return []math.LegacyDec{ds, dt}, nil
}

// entryHit is an entry a slash reached and the shares it took from the
// module's delegation at the destination.
type entryHit struct {
	entry  stakingtypes.RedelegationEntry
	shares math.LegacyDec
}

// slashedEntries replays x/staking's SlashRedelegation on a pair's entries
// (in its order) for each candidate fraction and each infraction height an
// entry boundary allows, the module's delegation holding `before` shares
// (its unbonding delegation at the destination is set aside, so nothing
// reduces a slash amount): an entry is slashed iff created at or after the
// infraction height, not mature, its slash amount trunc(f x initial balance)
// and its shares f x shares_dst both non-zero, and the delegation still
// there; it takes min(f x shares_dst, the delegation's shares). The replay
// that makes exactly `calls` unbonds of exactly `burnt` shares in all is the
// slash. Entries are in creation-height order (checkRedelegationRecord), so
// the entries an infraction height reaches are a suffix.
func slashedEntries(entries []stakingtypes.RedelegationEntry, now time.Time, fractions []math.LegacyDec,
	before, burnt math.LegacyDec, calls uint64,
) ([]entryHit, bool) {
	type eligible struct {
		at  int
		hit entryHit
	}
	// replay is x/staking's loop over entries[from:], the delegation's
	// shares capping each unbond (and gone once they reach 0).
	replay := func(el []eligible) ([]entryHit, math.LegacyDec) {
		running := before
		var hit []entryHit
		total := math.LegacyZeroDec()
		for _, x := range el {
			if !running.IsPositive() {
				break
			}
			sh := x.hit.shares
			if sh.GT(running) {
				sh = running
			}
			running = running.Sub(sh)
			total = total.Add(sh)
			hit = append(hit, entryHit{x.hit.entry, sh})
		}
		return hit, total
	}
	gone := burnt.Equal(before) // the module's delegation at dst is gone
	for _, f := range fractions {
		if !f.IsPositive() {
			continue
		}
		var el []eligible
		for i, e := range entries {
			if e.IsMature(now) && !e.OnHold() || f.MulInt(e.InitialBalance).TruncateInt().IsZero() {
				continue
			}
			if sh := f.Mul(e.SharesDst); !sh.IsZero() {
				el = append(el, eligible{i, entryHit{e, sh}})
			}
		}
		// From the latest boundary back: an infraction height above
		// entries[from-1]'s height and at most entries[from]'s reaches
		// entries[from:]. With the delegation still there, nothing capped
		// an unbond: the suffix's uncapped sum is what was burnt.
		first, sum := len(el), math.LegacyZeroDec()
		for from := len(entries) - 1; from >= 0; from-- {
			for first > 0 && el[first-1].at >= from {
				first--
				sum = sum.Add(el[first].hit.shares)
			}
			if from > 0 && entries[from-1].CreationHeight == entries[from].CreationHeight {
				continue
			}
			switch {
			case !gone && sum.GTE(before):
				// Past here the delegation would have run out.
			case !gone:
				if uint64(len(el)-first) == calls && sum.Equal(burnt) {
					hit := make([]entryHit, 0, calls)
					for _, x := range el[first:] {
						hit = append(hit, x.hit)
					}
					return hit, true
				}
			case sum.GTE(before):
				if hit, total := replay(el[first:]); uint64(len(hit)) == calls && total.Equal(burnt) {
					return hit, true
				}
			}
		}
	}
	return nil, false
}
