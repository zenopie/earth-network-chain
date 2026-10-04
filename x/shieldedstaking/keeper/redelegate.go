package keeper

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Private redelegation (MsgRedelegate).
//
// A staker moves d derth/A to validator B with no unbonding gap. The stake
// proof spends derth/A notes (v_out = d, change back to the owner); the chain
// moves their live value u = floor(d x B_A / S_A) from A's book to B's and
// mints derth/B worth what arrived, at B's live rate, to spc_mint (the same
// owner's: the stake circuit binds spc_mint to the spender).
//
// The value moves at once, never through an unbonding:
//
//  1. Rewards first. The module's unwithdrawn rewards at A and at B are
//     withdrawn into their queues (W -> P: neither backing changes), so the
//     x/staking calls below pay none outside the books.
//  2. Out of A's delegation queue first: P_A is ERTH waiting for the epoch
//     end to be delegated to A. min(u, P_A) moves to P_B as a book entry; it
//     was never bonded at A, so no x/staking entry is needed or made.
//  3. The rest from the module's bonded stake at A, with x/staking's
//     BeginRedelegate (module -> module, A -> B), under its rules: no
//     transitive redelegation (refused while stake redelegated INTO A
//     matures), at most max_entries maturing entries per (A, B). The rest
//     never exceeds D_A - U_A: B_A >= u with W_A = 0 and P_A spent.
//  4. B mints floor(arrived x S_B / B_B), arrived being the measured rise of
//     B's backing (u, less x/staking's truncation): rounding favours the
//     book on both sides.
//
// It runs in the private ante, atomically with the spend (ExecutesInAnte):
// anything x/staking refuses fails the tx before a note is spent or a fee
// paid. CheckPrivateAction refuses the same things earlier, before any
// proof is verified.
//
// Slashing. x/staking slashes a redelegation entry for an infraction of A
// committed before it (the entry's creation height is at or after the
// infraction height), within the unbonding period: it takes slash_fraction
// x the entry's initial balance from the module's unbonding entries at B
// begun after the infraction first, then from the module's delegation at B.
// B's book absorbs it: B's holders through B's rate, and B's undelegations
// begun after A's infraction through their payout. The redelegated notes
// are derth/B like any other (nothing links them to the redelegation), so
// the loss cannot follow them and is shared like any loss of B's. The
// value that moved out of A's queue was never bonded at A and carries no
// entry. See ORCHARD_DESIGN.md section 19.
//
// The module is ONE delegator to x/staking, so its limits are shared by
// every private staker: one redelegation into A blocks every redelegation
// out of A's bonded stake until it matures, and max_entries bounds the
// redelegations from A to B maturing at once (Query/Redelegation).

// gasRedelegate is MsgRedelegate's base gas: two reward withdrawals, the
// books, x/staking's BeginRedelegate (an unbond, a delegate, the entry and
// its queue) and the mint.
const gasRedelegate uint64 = 700_000

// liquidAt is v's value outside x/staking: its delegation queue and the
// module's unwithdrawn rewards there (withdrawn into the queue first).
func (k Keeper) liquidAt(ctx context.Context, valoper string, val sdk.ValAddress) (math.Int, error) {
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return math.Int{}, err
	}
	_, del, v, found, err := k.delegation(ctx, val)
	if err != nil {
		return math.Int{}, err
	}
	w := math.ZeroInt()
	if found && del.Shares.IsPositive() {
		if w, err = k.pendingRewards(ctx, v, del); err != nil {
			return math.Int{}, err
		}
	}
	return vs.PendingDelegation.Add(w), nil
}

// checkRedelegationLimits refuses what x/staking's BeginRedelegate would
// for the module from src to dst: a transitive redelegation, or one past
// max_entries for the pair. It names when the limit lifts.
func (k Keeper) checkRedelegationLimits(ctx context.Context, srcoper, dstoper string, src, dst sdk.ValAddress) error {
	if _, err := k.staking.GetValidator(ctx, src); err != nil {
		return types.ErrRedelegation.Wrapf("%s: %v", srcoper, err)
	}
	if recv, err := k.staking.HasReceivingRedelegation(ctx, k.modAddr, src); err != nil {
		return err
	} else if recv {
		until, err := k.receivingUntil(ctx, src)
		if err != nil {
			return err
		}
		return types.ErrRedelegation.Wrapf("private stake redelegated to %s matures until %s: x/staking refuses a transitive redelegation out of it until then (undelegating is not affected)",
			srcoper, time.Unix(0, until).UTC().Format(time.RFC3339))
	}
	if full, err := k.staking.HasMaxRedelegationEntries(ctx, k.modAddr, src, dst); err != nil {
		return err
	} else if full {
		n, earliest, err := k.pairEntries(ctx, src, dst)
		if err != nil {
			return err
		}
		return types.ErrRedelegation.Wrapf("%d private redelegations %s -> %s are maturing, x/staking's max_entries; the next completes at %s",
			n, srcoper, dstoper, time.Unix(0, earliest).UTC().Format(time.RFC3339))
	}
	return nil
}

// receivingUntil is when the last of the module's redelegation entries into
// val completes (unix ns; 0 if none).
func (k Keeper) receivingUntil(ctx context.Context, val sdk.ValAddress) (int64, error) {
	reds, err := k.staking.GetRedelegations(ctx, k.modAddr, ^uint16(0))
	if err != nil {
		return 0, err
	}
	var until int64
	for _, r := range reds {
		dst, err := k.staking.ValidatorAddressCodec().StringToBytes(r.ValidatorDstAddress)
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(dst, val) {
			continue
		}
		for _, e := range r.Entries {
			if t := e.CompletionTime.UnixNano(); t > until {
				until = t
			}
		}
	}
	return until, nil
}

// pairEntries is the module's maturing src -> dst entries and when the
// earliest completes (0 if none).
func (k Keeper) pairEntries(ctx context.Context, src, dst sdk.ValAddress) (int, int64, error) {
	red, err := k.staking.GetRedelegation(ctx, k.modAddr, src, dst)
	if errors.Is(err, stakingtypes.ErrNoRedelegation) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	var earliest int64
	for _, e := range red.Entries {
		if t := e.CompletionTime.UnixNano(); earliest == 0 || t < earliest {
			earliest = t
		}
	}
	return len(red.Entries), earliest, nil
}

// checkRedelegate refuses, read-only, what executeRedelegate would: the
// same validator twice, a destination this module will not delegate to (or
// whose book is settling), more derth than exists, a value or a mint below
// min_delegation or above one note, and an x/staking limit when the value
// does not fit in the source's queue. Returns the value u at the live rate.
func (k Keeper) checkRedelegate(ctx context.Context, m *types.MsgRedelegate) (math.Int, error) {
	src, err := k.valAddr(m.SrcValidator)
	if err != nil {
		return math.Int{}, err
	}
	dst, err := k.valAddr(m.DstValidator)
	if err != nil {
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
	minted, err := derthFor(u, bB, sB)
	if err != nil {
		return math.Int{}, err
	}
	if minted.LT(params.MinDelegation) {
		return math.Int{}, errorsmod.Wrapf(types.ErrAmount, "the redelegation mints %s derth, less than the minimum %s (rate %s)", minted, params.MinDelegation, rateOf(bB, sB))
	}
	if err := fitsNote(minted); err != nil {
		return math.Int{}, err
	}
	liquid, err := k.liquidAt(ctx, m.SrcValidator, src)
	if err != nil {
		return math.Int{}, err
	}
	if u.GT(liquid) {
		if err := k.checkRedelegationLimits(ctx, m.SrcValidator, m.DstValidator, src, dst); err != nil {
			return math.Int{}, err
		}
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

	// 3. The rest moves bonded, with x/staking's redelegation.
	var completion int64
	late := math.ZeroInt()
	if bonded.IsPositive() {
		if err := k.checkRedelegationLimits(ctx, m.SrcValidator, m.DstValidator, src, dst); err != nil {
			return nil, err
		}
		shares, err := k.staking.ValidateUnbondAmount(ctx, k.modAddr, src, bonded)
		if err != nil {
			return nil, errorsmod.Wrapf(types.ErrRedelegation, "%s from %s: %v", bonded, m.SrcValidator, err)
		}
		before := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
		t, err := k.staking.BeginRedelegation(ctx, k.modAddr, src, dst, shares)
		if err != nil {
			return nil, errorsmod.Wrapf(types.ErrRedelegation, "%s -> %s: %v", m.SrcValidator, m.DstValidator, err)
		}
		// Whatever the delegation changes paid (nothing: both were just
		// withdrawn) is the destination's, as every reward is booked.
		late = k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount.Sub(before)
		if t.After(ctx.BlockTime()) {
			completion = t.UnixNano()
		}
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
	// late (nothing, in practice) is the existing holders': it joins the
	// base the new derth is priced against, not what arrived.
	base := bB0.Add(late)
	arrived := bB1.Sub(base)
	minted, err := derthFor(arrived, base, sB0)
	if err != nil {
		return nil, err
	}
	if minted.LT(params.MinDelegation) {
		return nil, errorsmod.Wrapf(types.ErrAmount, "the redelegation mints %s derth, less than the minimum %s", minted, params.MinDelegation)
	}
	if vsB, err = k.ValidatorState(ctx, m.DstValidator); err != nil {
		return nil, err
	}
	if err := k.checkpointSupply(ctx, &vsB); err != nil {
		return nil, err
	}
	vsB.DerthSupply = vsB.DerthSupply.Add(minted)
	if err := k.Validators.Set(ctx, m.DstValidator, vsB); err != nil {
		return nil, err
	}

	// 6. The notes: the source's spent (change back), the destination's
	// minted to the owner.
	if _, err := k.applyStakeProof(ctx, &m.Stake); err != nil {
		return nil, err
	}
	pos, err := k.mintStake(ctx, types.DerthDenom(m.DstValidator), minted, m.Stake.SpcMint, m.Stake.SpcCiphertext)
	if err != nil {
		return nil, err
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
		sdk.NewAttribute(types.AttributeKeyMinted, minted.String()),
		sdk.NewAttribute(types.AttributeKeyQueued, queued.String()),
		sdk.NewAttribute(types.AttributeKeyBonded, bonded.String()),
		sdk.NewAttribute(types.AttributeKeyCompletion, completionAttr),
	))
	return &types.MsgRedelegateResponse{Value: u.Uint64(), Derth: minted.Uint64(), Position: pos, CompletionTime: completion}, nil
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
// BeforeValidatorModified, before it slashes src's redelegation entries):
// for every validator the module redelegated to from src, the module's
// rewards there are withdrawn into its queue, so the slash's unbond of the
// module's delegation pays none outside the books; and the validator is
// re-weighed at the end of the block (its rate may fall: B's book absorbs a
// slash of the entry). An operator's MsgEditValidator also gets here: the
// withdrawal is then harmless book-keeping and the re-weigh changes nothing
// (it only ever lowers an epoch rate to the live rate). Never fails.
func (k Keeper) prepareRedelegationSlash(ctx context.Context, src sdk.ValAddress) {
	reds, err := k.staking.GetRedelegationsFromSrcValidator(ctx, src)
	if err != nil {
		k.failure(ctx, "redelegation_slash", src.String(), err)
		return
	}
	seen := map[string]bool{}
	for _, r := range reds {
		if r.DelegatorAddress != k.modString(ctx) || seen[r.ValidatorDstAddress] {
			continue
		}
		seen[r.ValidatorDstAddress] = true
		dst, err := k.valAddr(r.ValidatorDstAddress)
		if err != nil {
			k.failure(ctx, "redelegation_slash", r.ValidatorDstAddress, err)
			continue
		}
		if err := k.guarded(ctx, func(cc context.Context) error { return k.collectRewards(cc, r.ValidatorDstAddress, dst) }); err != nil {
			k.failure(ctx, "redelegation_slash", r.ValidatorDstAddress, err)
		}
		if err := k.SlashedValidators.Set(ctx, r.ValidatorDstAddress); err != nil {
			k.failure(ctx, "slash_record", r.ValidatorDstAddress, err)
		}
	}
}

// modString is the module account's bech32 address.
func (k Keeper) modString(context.Context) string {
	s, err := k.addressCodec.BytesToString(k.modAddr)
	if err != nil {
		return ""
	}
	return s
}

// Redelegation reports x/staking's limits on a private redelegation from
// src to dst right now.
func (q queryServer) Redelegation(ctx context.Context, req *types.QueryRedelegationRequest) (*types.QueryRedelegationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	src, err := q.k.valAddr(req.SrcValidator)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	dst, err := q.k.valAddr(req.DstValidator)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	until, err := q.k.receivingUntil(ctx, src)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	n, earliest, err := q.k.pairEntries(ctx, src, dst)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	maxEntries, err := q.k.staking.MaxEntries(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	liquid, err := q.k.liquidAt(ctx, req.SrcValidator, src)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRedelegationResponse{
		SrcLockedUntil: until, Entries: uint32(n), MaxEntries: maxEntries, PairFreesAt: earliest, Queue: liquid,
	}, nil
}
