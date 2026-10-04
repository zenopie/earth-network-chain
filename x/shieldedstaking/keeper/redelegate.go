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
//  2. Out of A's delegation queue first: P_A is ERTH waiting for the epoch
//     end to be delegated to A. min(u, P_A) moves to P_B as a book entry; it
//     was never bonded at A, so no x/staking entry is needed or made.
//  3. The rest from the module's bonded stake at A, unbonded at A and
//     bonded at B at once with x/staking's primitives (moveBonded), the
//     redelegation entry recorded by the module itself: no transitive lock,
//     no max_entries. The rest never exceeds D_A - U_A: B_A >= u with W_A = 0
//     and P_A spent.
//  4. B credits dst_derth <= floor(arrived x S_B / B_B), arrived being the
//     measured rise of B's backing (u, less x/staking's truncation):
//     rounding, and what arrived buys beyond dst_derth, favour the book on
//     both sides (creditDst). A value exceeding A's queue by at most
//     bondedDust (0.001 ERTH) moves out of the queue alone, the excess left
//     to A's book.
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
// aside for the slash). The value that moved out of A's queue was never
// bonded at A and carries no entry. See ORCHARD_DESIGN.md sections 19 and
// 20.

// bondedDust is the most a redelegation's value may exceed the source's
// queue by and still move out of the queue alone, the excess left to the
// source's book: x/staking may return nothing for so few tokens of a
// slashed validator (and would refuse the redelegation), and an entry for
// it would take one of the pair's max_entries.
var bondedDust = math.NewInt(1_000)

// gasRedelegate is MsgRedelegate's base gas: two reward withdrawals, the
// books, the bonded move (an unbond, a delegate, the entry and its queue)
// and the move's record.
const gasRedelegate uint64 = 700_000

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
	// at most 1/min_delegation of it, as for a delegation (audit F4).
	if u.LT(params.MinDelegation) {
		return math.Int{}, errorsmod.Wrapf(types.ErrAmount, "the redelegation is worth %s%s, less than the minimum %s", u, types.BondDenom, params.MinDelegation)
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
// proof's notes and mints derth/<dst>. Runs in the private ante after the
// fee bundle was spent; any error fails the whole tx, spend included.
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
	vsA, err := k.ValidatorState(ctx, m.SrcValidator)
	if err != nil {
		return nil, err
	}
	queued := math.MinInt(u, vsA.PendingDelegation)
	bonded := u.Sub(queued)
	if bonded.IsPositive() && bonded.LTE(bondedDust) {
		// Dust beyond the queue stays with the source's book.
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
	if vsA, err = k.ValidatorState(ctx, m.SrcValidator); err != nil {
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

	// 7. The move, while a slash of the source can reach its entry.
	if entryHeight != 0 {
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
