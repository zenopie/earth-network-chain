package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Private redelegation (MsgRedelegate).
//
// A staker moves d derth/A to validator B with no unbonding gap. The stake
// proof spends derth/A notes (v_out = d, change back to the owner); the chain
// moves their live value u = floor(d x B_A / S_A) from A's book to B's and
// credits the msg's dst_derth of derth/B, checked to be at most what
// arrived buys at B's live rate, to the owner's derth/B note: the proof's
// credit lane spends that note (or pads) and creates the merged one,
// labelled with the move (moves.go), so derth/B stays with the spender (the
// circuit binds every output to the spender's owner_pk).
//
// The value moves at once, never through an unbonding:
//
//  1. Rewards first. The module's unwithdrawn rewards at A and at B are
//     withdrawn into their queues (W -> P: neither backing changes), so the
//     x/staking calls below pay none outside the books.
//  2. Pro rata to A's book (audit 7, A7-1). Every derth/A is a claim on
//     B_A = (D_A - U_A) + P_A with W_A = 0: the bonded part D_A - U_A, which
//     a slash of A burns, and the queue P_A, ERTH waiting for the epoch end
//     to be delegated to A, which no slash reaches. The value u leaves both
//     parts in that proportion: floor(u x P_A / B_A) out of the queue, as a
//     book entry, and the rest bonded. Taking the queue first would let a
//     staker who sees a slash of A coming (missed blocks, double-sign
//     evidence in the pool, the operator itself) leave with the unslashable
//     slice of the book and its remaining holders pay their share. The
//     bonded part never exceeds D_A - U_A (u <= B_A).
//     Only when no slash can reach A's stake does the value come out of the
//     queue first: A is unbonded (x/staking does not slash an unbonded
//     validator, and x/evidence ignores evidence against one), or its bonded
//     part is nothing (D_A <= U_A: slashed to nothing, or never delegated);
//     the rest, if any, then moves bonded with no entry, as x/staking's
//     BeginRedelegation from an unbonded validator does.
//  3. The bonded part is unbonded at A and bonded at B at once with
//     x/staking's primitives (moveBonded), the redelegation entry recorded
//     by the module itself: no transitive lock, no max_entries. A slash of A
//     for an infraction before the move reaches the entry, and through it
//     the move's label (moves.go: the slash debt).
//  4. B credits dst_derth <= floor(arrived x S_B / B_B), arrived being the
//     measured rise of B's backing (u, less x/staking's truncation):
//     rounding, and what arrived buys beyond dst_derth, favour the book on
//     both sides (creditDst). A bonded part of at most bondedDust (0.001
//     ERTH) does not move: the value is the queued part alone, the dust left
//     to A's book (the mover's loss, never the book's).
//
// It runs in the private ante, atomically with the spend (ExecutesInAnte):
// anything refused fails the tx before a note is spent or a fee paid.
// CheckPrivateAction refuses the same things earlier, before any proof is
// verified.
//
// Slashing. x/staking slashes the module's redelegation entry for an
// infraction of A committed before it while it matures: slash_fraction x
// the entry's shares at B, unbonded from the module's delegation at B and
// burnt. The module covers it so B's rate does not move, and the moves of
// the slashed entries owe it through their notes' labels (moves.go: the
// slash debt). B's undelegations already under way are spared: x/staking
// would take the slash from them first (prepareRedelegationSlash sets them
// aside for the slash). The part that moved out of A's queue was never
// bonded at A and carries no entry: it is the move's pro-rata share of what
// a slash of A could not take either. See ORCHARD_DESIGN.md 8.7.

// bondedDust is the largest bonded part a redelegation leaves behind: the
// value is then its queued part alone, the dust left to the source's book.
// x/staking may return nothing for so few tokens of a slashed validator (and
// the redelegation would be refused), and an entry for it would cost the
// pair a slot.
var bondedDust = math.NewInt(1_000)

// gasRedelegate is MsgRedelegate's base gas: two reward withdrawals, the
// books, the bonded move (an unbond, a delegate, the entry and its queue)
// and the move's record.
const gasRedelegate uint64 = 700_000

// gasPerRedelegationEntry is what each entry of the pair's x/staking record
// adds: a bonded move reads the record twice and writes it whole (x/staking
// keeps a pair's entries in one record; ~70 bytes an entry, at 3 gas a byte
// read and 30 written, and its decoding). gasMergeEntries is added while
// the pair holds MaxEntryHeightsPerPair entries: the merge writes the record
// once more and re-files up to MaxMergeMoves moves (audit 7, A7-L1).
const (
	gasPerRedelegationEntry uint64 = 2_500
	gasPerMergedEntry       uint64 = 2_500
	gasMergeMove            uint64 = 20_000
)

// redelegateGas is MsgRedelegate's gas before its proof and writes: the base,
// plus the pair's record at its current size (it may grow by the block's
// other moves before this one runs: a wallet simulates it, with headroom).
func (k Keeper) redelegateGas(ctx context.Context, m *types.MsgRedelegate) (uint64, error) {
	src, err := k.valAddr(m.SrcValidator)
	if err != nil {
		return 0, err
	}
	dst, err := k.valAddr(m.DstValidator)
	if err != nil {
		return 0, err
	}
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, src, dst)
	if errors.Is(err, stakingtypes.ErrNoRedelegation) {
		return gasRedelegate, nil
	} else if err != nil {
		return 0, err
	}
	n := uint64(len(red.Entries))
	g := gasRedelegate + n*gasPerRedelegationEntry
	if countedEntries(red.Entries) >= types.MaxEntryHeightsPerPair {
		g += n*gasPerMergedEntry + types.MaxMergeMoves*gasMergeMove
	}
	return g, nil
}

// checkRedelegate refuses, read-only, what executeRedelegate would: the
// same validator twice, a destination this module will not delegate to (or
// whose book is settling), more derth than exists, a value or a credit below
// min_delegation or above one note, a credit the value does not buy, and a
// move_time out of range. Returns the value u at the live rate.
func (k Keeper) checkRedelegate(ctx context.Context, m *types.MsgRedelegate) (math.Int, error) {
	src, err := k.valAddr(m.SrcValidator)
	if err != nil {
		return math.Int{}, err
	}
	dst, err := k.valAddr(m.DstValidator)
	if err != nil {
		return math.Int{}, err
	}
	if err := checkMoveTime(ctx, m.MoveTime); err != nil {
		return math.Int{}, err
	}
	if bytes.Equal(src, dst) {
		return math.Int{}, types.ErrRedelegation.Wrap("source and destination are the same validator")
	}
	if err := k.checkDelegatable(ctx, m.DstValidator); err != nil {
		return math.Int{}, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return math.Int{}, err
	}
	bB, sB, err := k.Backing(ctx, m.DstValidator)
	if err != nil {
		return math.Int{}, err
	}
	if !sB.IsPositive() && bB.IsPositive() {
		// As for a delegation (audit F5): the epoch end settles it first.
		return math.Int{}, types.ErrValidator.Wrapf("%s's book is settling (no derth, %s%s backing); redelegate after the epoch end", m.DstValidator, bB, types.BondDenom)
	}
	bA, sA, err := k.Backing(ctx, m.SrcValidator)
	if err != nil {
		return math.Int{}, err
	}
	d := math.NewIntFromUint64(m.Amount)
	if d.GT(sA) {
		return math.Int{}, errorsmod.Wrap(types.ErrAmount, "more derth than exists")
	}
	u := valueOf(d, bA, sA)
	// At least min_delegation, worth and minted: the rounding loss is then
	// at most 1/min_delegation of it, as for a delegation (audit F4). At
	// most one note's worth, as for an undelegation (the response carries
	// it as a uint64).
	if u.LT(params.MinDelegation) {
		return math.Int{}, errorsmod.Wrapf(types.ErrAmount, "the redelegation is worth %s%s, less than the minimum %s", u, types.BondDenom, params.MinDelegation)
	}
	if err := fitsNote(u); err != nil {
		return math.Int{}, err
	}
	// An estimate (x/staking may truncate what arrives by a uerth):
	// executeRedelegate checks the arrival itself, in the ante.
	buys, err := derthFor(u, bB, sB)
	if err != nil {
		return math.Int{}, err
	}
	if err := checkCredit("redelegation", math.NewIntFromUint64(m.DstDerth), buys, params.MinDelegation, rateOf(bB, sB)); err != nil {
		return math.Int{}, err
	}
	return u, nil
}

// splitValue splits the value u leaving src's book into the part that comes
// out of its queue and the part that moves bonded (step 2 above): pro rata to
// the book's queue P and bonded part D - U, the queued part rounded down (so
// the slashable part rounds up); out of the queue first only while no slash
// can reach src's stake (src unbonded, or no bonded part). Run after the
// rewards were collected (W = 0, so u <= D - U + P).
func (k Keeper) splitValue(ctx context.Context, src sdk.ValAddress, srcoper string, u math.Int) (queued, bonded math.Int, err error) {
	vs, err := k.ValidatorState(ctx, srcoper)
	if err != nil {
		return math.Int{}, math.Int{}, err
	}
	p := vs.PendingDelegation
	d, _, v, found, err := k.delegation(ctx, src)
	if err != nil {
		return math.Int{}, math.Int{}, err
	}
	slashable := d.Sub(vs.PendingUndelegation)
	if !found || v.IsUnbonded() || !slashable.IsPositive() {
		queued = math.MinInt(u, p)
		return queued, u.Sub(queued), nil
	}
	queued = u.Mul(p).Quo(slashable.Add(p))
	bonded = u.Sub(queued)
	if bonded.GT(slashable) {
		// Unreachable while u <= B: never more than the bonded part.
		bonded = slashable
		queued = u.Sub(bonded)
	}
	if queued.GT(p) {
		return math.Int{}, math.Int{}, errorsmod.Wrapf(types.ErrRedelegation, "the value %s exceeds %s's book", u, srcoper)
	}
	return queued, bonded, nil
}

// collectRewards withdraws the module's rewards at val into v's queue (a
// book with a delegation and no rewards is left as it is).
func (k Keeper) collectRewards(ctx context.Context, valoper string, val sdk.ValAddress) error {
	_, del, _, found, err := k.delegation(ctx, val)
	if err != nil || !found || !del.Shares.IsPositive() {
		return err
	}
	before := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
	if _, err := k.distr.WithdrawDelegationRewards(ctx, k.modAddr, val); err != nil {
		return err
	}
	r := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount.Sub(before)
	if !r.IsPositive() {
		return nil
	}
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return err
	}
	vs.PendingDelegation = vs.PendingDelegation.Add(r)
	return k.Validators.Set(ctx, valoper, vs)
}

// executeRedelegate moves the value (see the top of this file), spends the
// proof's notes and credits derth/<dst> to the proof's merged dst note. Runs
// in the private ante after the fee bundle was spent; any error fails the
// whole tx, spend included.
func (k Keeper) executeRedelegate(ctx sdk.Context, m *types.MsgRedelegate) (*types.MsgRedelegateResponse, error) {
	if _, err := k.checkRedelegate(ctx, m); err != nil {
		return nil, err
	}
	src, _ := k.valAddr(m.SrcValidator)
	dst, _ := k.valAddr(m.DstValidator)
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	// 1. Rewards into the queues: no x/staking call below pays any.
	if err := k.collectRewards(ctx, m.SrcValidator, src); err != nil {
		return nil, err
	}
	if err := k.collectRewards(ctx, m.DstValidator, dst); err != nil {
		return nil, err
	}

	// 2. The value at the source's live rate, and the destination before.
	bA, sA, err := k.Backing(ctx, m.SrcValidator)
	if err != nil {
		return nil, err
	}
	d := math.NewIntFromUint64(m.Amount)
	u := valueOf(d, bA, sA)
	bB0, sB0, err := k.Backing(ctx, m.DstValidator)
	if err != nil {
		return nil, err
	}
	queued, bonded, err := k.splitValue(ctx, src, m.SrcValidator, u)
	if err != nil {
		return nil, err
	}
	if bonded.IsPositive() && bonded.LTE(bondedDust) {
		// A dust bonded part stays with the source's book.
		u, bonded = queued, math.ZeroInt()
	}

	// 3. The rest moves bonded, at once, its x/staking entry recorded.
	var completion, entryHeight int64
	shares := math.LegacyZeroDec()
	late := math.ZeroInt()
	if bonded.IsPositive() {
		before := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
		if shares, entryHeight, completion, err = k.moveBonded(ctx, src, dst, bonded); err != nil {
			return nil, err
		}
		// Whatever the delegation changes paid (nothing: both were just
		// withdrawn) is the destination's, as every reward is booked.
		late = k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount.Sub(before)
	}

	// 4. The books: the source's queue and supply, the destination's queue.
	vsA, err := k.ValidatorState(ctx, m.SrcValidator)
	if err != nil {
		return nil, err
	}
	if err := k.checkpointSupply(ctx, &vsA); err != nil {
		return nil, err
	}
	vsA.PendingDelegation = vsA.PendingDelegation.Sub(queued)
	vsA.DerthSupply = vsA.DerthSupply.Sub(d)
	if err := k.Validators.Set(ctx, m.SrcValidator, vsA); err != nil {
		return nil, err
	}
	vsB, err := k.ValidatorState(ctx, m.DstValidator)
	if err != nil {
		return nil, err
	}
	vsB.PendingDelegation = vsB.PendingDelegation.Add(queued).Add(late)
	if err := k.Validators.Set(ctx, m.DstValidator, vsB); err != nil {
		return nil, err
	}

	// 5. derth/<dst> for what arrived, at the destination's rate before.
	bB1, _, err := k.Backing(ctx, m.DstValidator)
	if err != nil {
		return nil, err
	}
	credited, err := k.creditDst(ctx, m, bB0, sB0, bB1, late, params.MinDelegation)
	if err != nil {
		return nil, err
	}

	// 6. The notes: the source's spent (the change back), the owner's
	// derth/<dst> note spent (or padded) and the merged, labelled one created.
	positions, err := k.applyStakeProof(ctx, &m.Stake)
	if err != nil {
		return nil, err
	}
	var pos uint64
	if len(positions) > 0 {
		pos = positions[len(positions)-1]
	}

	// Both books' Groundworks voters are re-filed at the end of the block
	// (reweighSlashed), so neither keeps weight at a rate the move changed
	// (audit 7).
	if err := k.SlashedValidators.Set(ctx, m.SrcValidator); err != nil {
		return nil, err
	}
	if err := k.SlashedValidators.Set(ctx, m.DstValidator); err != nil {
		return nil, err
	}

	// 7. The move, while a slash of the source can reach its entry (an
	// entry has a completion; its height may be 0 for a source unbonding
	// since a zero-height export).
	if completion != 0 {
		mv := types.Move{
			Key: m.MoveKey(), SrcValidator: m.SrcValidator, DstValidator: m.DstValidator,
			Height: ctx.BlockHeight(), MoveTime: m.MoveTime, Credited: credited, Shares: shares,
			EntryHeight: entryHeight, Completion: completion, Retained: credited,
		}
		if err := k.putMove(ctx, mv); err != nil {
			return nil, err
		}
	}
	completionAttr := ""
	if completion > 0 {
		completionAttr = strconv.FormatInt(completion, 10)
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeRedelegate,
		sdk.NewAttribute(types.AttributeKeySrcValidator, m.SrcValidator),
		sdk.NewAttribute(types.AttributeKeyDstValidator, m.DstValidator),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
		sdk.NewAttribute(types.AttributeKeyValue, u.String()),
		sdk.NewAttribute(types.AttributeKeyCredited, credited.String()),
		sdk.NewAttribute(types.AttributeKeyQueued, queued.String()),
		sdk.NewAttribute(types.AttributeKeyBonded, bonded.String()),
		sdk.NewAttribute(types.AttributeKeyCompletion, completionAttr),
		sdk.NewAttribute(types.AttributeKeyMoveKey, hex.EncodeToString(m.MoveKey())),
		sdk.NewAttribute(types.AttributeKeyMoveTime, strconv.FormatUint(m.MoveTime, 10)),
	))
	return &types.MsgRedelegateResponse{Value: u.Uint64(), Derth: credited.Uint64(), Position: pos, CompletionTime: completion}, nil
}

// creditDst credits the msg's dst_derth to the destination's book: it must
// be at most what arrived buys at the destination's rate before the move
// (bB0 and sB0; arrived is the rise of its backing to bB1, less late, the
// existing holders' rewards paid by the move itself). What arrived buys
// beyond dst_derth stays in the book. The note side is the proof's credit
// lane (applyStakeProof): the owner's derth/<dst> note merged with
// dst_derth. Kept apart from how the value moves, which a later redesign of
// the redelegation's internals may replace.
func (k Keeper) creditDst(ctx sdk.Context, m *types.MsgRedelegate, bB0, sB0, bB1, late, min math.Int) (math.Int, error) {
	// late (nothing, in practice) is the existing holders': it joins the
	// base the new derth is priced against, not what arrived.
	base := bB0.Add(late)
	arrived := bB1.Sub(base)
	buys, err := derthFor(arrived, base, sB0)
	if err != nil {
		return math.Int{}, err
	}
	credited := math.NewIntFromUint64(m.DstDerth)
	if err := checkCredit("redelegation", credited, buys, min, rateOf(base, sB0)); err != nil {
		return math.Int{}, err
	}
	vsB, err := k.ValidatorState(ctx, m.DstValidator)
	if err != nil {
		return math.Int{}, err
	}
	if err := k.checkpointSupply(ctx, &vsB); err != nil {
		return math.Int{}, err
	}
	vsB.DerthSupply = vsB.DerthSupply.Add(credited)
	if err := k.Validators.Set(ctx, m.DstValidator, vsB); err != nil {
		return math.Int{}, err
	}
	return credited, nil
}

// Redelegate returns what the private ante's run of the redelegation did.
func (k msgServer) Redelegate(ctx context.Context, m *types.MsgRedelegate) (*types.MsgRedelegateResponse, error) {
	res, executed, err := shieldedkeeper.AuthorizedResult(ctx, m)
	if err != nil {
		return nil, err
	}
	r, ok := res.(*types.MsgRedelegateResponse)
	if !executed || !ok {
		return nil, shieldedtypes.ErrUnauthorized.Wrap("the redelegation was not executed by the private ante")
	}
	return r, nil
}

// ExecutesInAnte: a redelegation runs atomically with its spend, so that
// x/staking's refusal (its limits are shared by every private staker, so
// they can change between CheckTx and the block) costs nothing.
func (h ActionHandler) ExecutesInAnte(msg shieldedtypes.PrivateMsg) bool {
	_, ok := msg.(*types.MsgRedelegate)
	return ok
}

// ExecutePrivateAction runs a redelegation for the ante.
func (h ActionHandler) ExecutePrivateAction(ctx sdk.Context, msg shieldedtypes.PrivateMsg, _ any) (any, error) {
	m, ok := msg.(*types.MsgRedelegate)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidMsg, "%T does not run in the ante", msg)
	}
	return h.k.executeRedelegate(ctx, m)
}

// prepareRedelegationSlash runs as a slash of src begins (x/staking's
// BeforeValidatorModified, called before it slashes src's redelegation
// entries; MsgEditValidator gets here too, in a tx, and is ignored). For
// every validator dst the module redelegated to from src:
//
//   - the module's rewards at dst are withdrawn into dst's queue, so the
//     slash's unbond of the module's delegation there pays none outside the
//     books;
//   - the module's x/staking unbonding delegation at dst is set aside
//     (ShelteredUnbondings) until the slash is over. x/staking takes a
//     redelegation entry's slash from the delegator's unbonding entries at
//     the destination begun after the infraction FIRST, and then still the
//     full slash_fraction x shares from its delegation there unless those
//     entries covered all of it. For one person that is their own
//     undelegation; for this module those entries are dst's undelegations
//     (other people, who left dst and never staked at src), and the slash
//     would take from them up to the whole amount besides. Set aside, the
//     slash takes exactly slash_fraction x the entry's shares from the
//     module's delegation at dst: dst's book absorbs it, pro rata;
//   - the slash is watched (openSlashWatch): what it burns at dst becomes the
//     slash debt of the moves it reached (moves.go);
//   - dst is re-weighed at the end of the block.
//
// restoreSheltered puts the unbonding delegations back: at the start of the
// next slash (before anything else, so a slash of dst itself sees its own
// undelegations), in this module's BeginBlocker (after x/slashing's and
// x/evidence's, the only callers of Slash: so no tx ever sees them set
// aside) and, defensively, at the start of EndBlock. Never fails a slash.
func (k Keeper) prepareRedelegationSlash(ctx context.Context, src sdk.ValAddress) {
	k.restoreSheltered(ctx)
	// Slashes run in block hooks (x/slashing's and x/evidence's
	// BeginBlockers), never in a tx: a tx getting here is MsgEditValidator,
	// which slashes nothing.
	if len(sdk.UnwrapSDKContext(ctx).TxBytes()) != 0 {
		return
	}
	reds, err := k.staking.GetRedelegationsFromSrcValidator(ctx, src)
	if err != nil {
		k.failure(ctx, "redelegation_slash", src.String(), err)
		return
	}
	k.openSlashWatch(ctx, src, reds)
	mod := k.modString(ctx)
	seen := map[string]bool{}
	for _, r := range reds {
		if r.DelegatorAddress != mod || seen[r.ValidatorDstAddress] {
			continue
		}
		seen[r.ValidatorDstAddress] = true
		dstoper := r.ValidatorDstAddress
		dst, err := k.valAddr(dstoper)
		if err != nil {
			k.failure(ctx, "redelegation_slash", dstoper, err)
			continue
		}
		if err := k.guarded(ctx, func(cc context.Context) error {
			if err := k.collectRewards(cc, dstoper, dst); err != nil {
				return err
			}
			return k.shelterUnbondings(cc, dst)
		}); err != nil {
			k.failure(ctx, "redelegation_slash", dstoper, err)
		}
		if err := k.SlashedValidators.Set(ctx, dstoper); err != nil {
			k.failure(ctx, "slash_record", dstoper, err)
		}
	}
}

// shelterUnbondings sets the module's unbonding delegation at dst aside.
func (k Keeper) shelterUnbondings(ctx context.Context, dst sdk.ValAddress) error {
	ubd, err := k.staking.GetUnbondingDelegation(ctx, k.modAddr, dst)
	if errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) {
		return nil
	} else if err != nil {
		return err
	}
	if err := k.ShelteredUnbondings.Set(ctx, dst, ubd); err != nil {
		return err
	}
	return k.staking.RemoveUnbondingDelegation(ctx, ubd)
}

// restoreSheltered puts every unbonding delegation set aside back, as it
// was. A failure is reported and retried at the next call (the entry stays
// sheltered, never lost).
func (k Keeper) restoreSheltered(ctx context.Context) {
	var held [][]byte
	_ = k.ShelteredUnbondings.Walk(ctx, nil, func(dst []byte, _ stakingtypes.UnbondingDelegation) (bool, error) {
		held = append(held, dst)
		return false, nil
	})
	for _, dst := range held {
		if err := k.guarded(ctx, func(cc context.Context) error {
			ubd, err := k.ShelteredUnbondings.Get(cc, dst)
			if err != nil {
				return err
			}
			if err := k.staking.SetUnbondingDelegation(cc, ubd); err != nil {
				return err
			}
			return k.ShelteredUnbondings.Remove(cc, dst)
		}); err != nil {
			k.failure(ctx, "redelegation_slash_restore", sdk.ValAddress(dst).String(), err)
		}
	}
}

// BeginBlocker settles the last slash watched this block and puts back the
// unbonding delegations a slash set aside (it runs after x/slashing and
// x/evidence, before any tx).
func (k Keeper) BeginBlocker(ctx context.Context) error {
	k.finishSlashWatch(ctx)
	k.restoreSheltered(ctx)
	return nil
}

// modString is the module account's bech32 address.
func (k Keeper) modString(context.Context) string {
	s, err := k.addressCodec.BytesToString(k.modAddr)
	if err != nil {
		return ""
	}
	return s
}
