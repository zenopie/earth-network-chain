package keeper

import (
	"bytes"
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Private actions: what the x/shielded ante runs for this module's msgs
// before it spends their bundles (see x/shielded/types.PrivateActionHandler).
// Every msg carries a stake proof (circuits/stake) over this module's stake
// note tree; the ante runs Check (state: the proof's shape for the msg, its
// nullifiers unspent, its anchor, the msg's own checks) and Verify (the
// proof, against the msg's sighash) before it spends the fee bundle.
//
// Check refuses everything the msg's handler would refuse, because the ante's
// spend stands even when the handler fails: a refused Delegate after the ante
// released its ERTH would leave that ERTH in the pool with no note for it.
// The handler repeats the same checks on the same state (both run inside one
// DeliverTx), so it cannot disagree.
//
// Gas is fixed per msg type and prepaid with the bundles', and handlers run
// their effects on an infinite meter, as x/shielded's MsgSend does. An
// unsigned tx's gas limit can be rewritten by whoever relays it; with metered
// effects they could pick a limit that passes the ante and runs out in the
// handler, after the notes are spent.

// Base gas per action, on top of the bundles', the stake proof's verification
// and one note write per stake note or nullifier the action writes. Covers
// the handler's reads and writes (the rate's reward computation is the
// heaviest: a distribution period walk).
const (
	gasDelegate   uint64 = 400_000
	gasRestake    uint64 = 100_000
	gasUndelegate uint64 = 400_000
	gasClaim      uint64 = 250_000
	gasVote       uint64 = 250_000
	gasLock       uint64 = 400_000
	gasUpdate     uint64 = 300_000
	gasUnlock     uint64 = 300_000
	gasPosVote    uint64 = 250_000
)

// prepared is what Check derived: the sighash the stake proof binds, its
// public asset and v_out. Handlers recompute what else they need.
type prepared struct {
	sighash fr.Element
	asset   fr.Element
	vOut    uint64
}

// ActionHandler implements x/shielded's PrivateActionHandler for every
// private msg of this module.
type ActionHandler struct{ k Keeper }

// NewActionHandler returns the handler registered for this module's msgs.
func NewActionHandler(k Keeper) ActionHandler { return ActionHandler{k: k} }

// RegisterPrivateActions registers h for each of this module's private msgs.
func RegisterPrivateActions(register func(string, shieldedtypes.PrivateActionHandler), h ActionHandler) {
	for _, t := range []string{
		types.TypeMsgDelegate, types.TypeMsgRestake, types.TypeMsgUndelegate, types.TypeMsgClaimUnbonding,
		types.TypeMsgStakeVote, types.TypeMsgLockPosition, types.TypeMsgUpdatePosition, types.TypeMsgUnlockPosition,
		types.TypeMsgPositionVote,
	} {
		register(t, h)
	}
}

func (h ActionHandler) PrivateActionGas(ctx context.Context, msg shieldedtypes.PrivateMsg) (uint64, error) {
	proof, note, err := h.k.shielded.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	sm, ok := msg.(types.StakeMsg)
	if !ok {
		return 0, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	// The proof, and a write per nullifier and output it carries, plus one
	// for a note the chain mints.
	writes := uint64(len(sm.StakeProofOf().Nullifiers)+len(sm.StakeProofOf().Commitments)) + 1
	var base uint64
	switch msg.(type) {
	case *types.MsgDelegate:
		base = gasDelegate
	case *types.MsgRestake:
		base = gasRestake
	case *types.MsgUndelegate:
		base = gasUndelegate
	case *types.MsgClaimUnbonding:
		base = gasClaim
	case *types.MsgStakeVote:
		base = gasVote
	case *types.MsgLockPosition:
		base = gasLock
	case *types.MsgUpdatePosition:
		base = gasUpdate
	case *types.MsgUnlockPosition:
		base = gasUnlock
	case *types.MsgPositionVote:
		base = gasPosVote
	default:
		return 0, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	return base + proof + writes*note, nil
}

func (h ActionHandler) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	k := h.k
	sm, ok := msg.(types.StakeMsg)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	var err error
	switch m := msg.(type) {
	case *types.MsgDelegate:
		_, err = k.checkDelegate(ctx, m)
	case *types.MsgRestake:
		_, err = k.valAddr(m.Validator)
	case *types.MsgUndelegate:
		_, err = k.checkUndelegate(ctx, m)
	case *types.MsgClaimUnbonding:
		_, _, err = k.checkClaim(ctx, m)
	case *types.MsgStakeVote:
		_, err = k.checkStakeVote(ctx, m)
	case *types.MsgLockPosition:
		err = k.checkLock(ctx, m)
	case *types.MsgUpdatePosition:
		_, err = k.checkUpdate(ctx, m)
	case *types.MsgUnlockPosition:
		_, err = k.checkPositionOwner(ctx, m.PositionId, &m.Stake)
	case *types.MsgPositionVote:
		_, _, err = k.checkPositionVote(ctx, m)
	}
	if err != nil {
		return nil, err
	}
	p := sm.StakeProofOf()
	if err := k.checkStakeNullifiers(ctx, p.SpentNullifiers()); err != nil {
		return nil, err
	}
	// A stake vote's anchor is its proposal's snapshot root (checked in
	// checkStakeVote); every other proof that spends proves against the
	// window. A proof that spends nothing proves no membership.
	if _, vote := msg.(*types.MsgStakeVote); !vote && len(p.SpentNullifiers()) > 0 {
		if err := k.checkStakeAnchor(ctx, p.Anchor); err != nil {
			return nil, err
		}
	}
	cms, _ := p.Outputs()
	if err := k.checkStakeCapacity(ctx, uint64(len(cms))+1); err != nil {
		return nil, err
	}
	sighash, err := shieldedtypes.SighashOf(ctx, msg, k.addressCodec)
	if err != nil {
		return nil, err
	}
	return prepared{sighash: sighash, asset: types.StakeAsset(sm.StakeDenom()), vOut: sm.VOut()}, nil
}

// VerifyPrivateAction verifies the stake proof against the msg's sighash.
func (h ActionHandler) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, pr any) error {
	sm := msg.(types.StakeMsg)
	p := pr.(prepared)
	sp := sm.StakeProofOf()
	if err := h.k.shielded.VerifyCircuit(ctx, shieldedtypes.CircuitStake, sp.Proof, sp.PublicInputs(p.asset, p.vOut, p.sighash)); err != nil {
		return errorsmod.Wrap(types.ErrInvalidStakeProof, err.Error())
	}
	return nil
}

// ExecutesInAnte: a claim runs in the ante, atomically with its spend, so it
// can pay its fee from the ERTH it claims (see
// x/shielded/types.PrivateActionExecutor). One path whether or not it does.
func (h ActionHandler) ExecutesInAnte(msg shieldedtypes.PrivateMsg) bool {
	_, ok := msg.(*types.MsgClaimUnbonding)
	return ok
}

// ExecutePrivateAction runs a claim for the ante.
func (h ActionHandler) ExecutePrivateAction(ctx sdk.Context, msg shieldedtypes.PrivateMsg, _ any) (any, error) {
	m, ok := msg.(*types.MsgClaimUnbonding)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidMsg, "%T does not run in the ante", msg)
	}
	return h.k.executeClaim(ctx, m)
}

// authorized is the handlers' gate: the ante checked this very msg's action
// and executed its bundles in this tx. Returns ctx on an infinite meter.
func (k Keeper) authorized(ctx context.Context, msg shieldedtypes.PrivateMsg) (sdk.Context, error) {
	if _, err := shieldedkeeper.AuthorizedAction(ctx, msg); err != nil {
		return sdk.Context{}, err
	}
	return sdk.UnwrapSDKContext(ctx).WithGasMeter(storetypes.NewInfiniteGasMeter()), nil
}

// ---- checks ---------------------------------------------------------------

func fitsNote(v math.Int) error {
	if !v.IsPositive() || !v.IsUint64() {
		return errorsmod.Wrapf(types.ErrAmount, "%s", v)
	}
	return nil
}

// checkDelegate returns the derth the delegation mints.
func (k Keeper) checkDelegate(ctx context.Context, m *types.MsgDelegate) (math.Int, error) {
	if err := k.checkDelegatable(ctx, m.Validator); err != nil {
		return math.Int{}, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return math.Int{}, err
	}
	amount := math.NewIntFromUint64(m.Delegated())
	if amount.LT(params.MinDelegation) {
		return math.Int{}, errorsmod.Wrapf(types.ErrAmount, "a delegation is at least %s%s", params.MinDelegation, types.BondDenom)
	}
	b, s, err := k.Backing(ctx, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	if !s.IsPositive() && b.IsPositive() {
		// Backing nobody owns (rewards accrued after the last holder's
		// notes were minted): the epoch end settles it (processValidator).
		// A delegation now would buy it at rate 1 (audit F5).
		return math.Int{}, types.ErrValidator.Wrapf("%s's book is settling (no derth, %s%s backing); delegate after the epoch end", m.Validator, b, types.BondDenom)
	}
	d, err := derthFor(amount, b, s)
	if err != nil {
		return math.Int{}, err
	}
	// At least min_delegation derth: a delegation's rounding loss (under one
	// derth's value) is then at most 1/min_delegation of it, however far a
	// donation to the validator's rewards pool has pushed the rate (audit
	// F4); a delegation too small for that is refused, not rounded away.
	if d.LT(params.MinDelegation) {
		return math.Int{}, errorsmod.Wrapf(types.ErrAmount, "the delegation mints %s derth, less than the minimum %s (rate %s)", d, params.MinDelegation, rateOf(b, s))
	}
	if err := fitsNote(d); err != nil {
		return math.Int{}, err
	}
	return d, nil
}

// checkUndelegate returns the claim's value.
func (k Keeper) checkUndelegate(ctx context.Context, m *types.MsgUndelegate) (math.Int, error) {
	if _, err := k.valAddr(m.Validator); err != nil {
		return math.Int{}, err
	}
	b, s, err := k.Backing(ctx, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	// This epoch's record must still be open (the epoch-end sweep only
	// settles records of ended epochs; never reached, but cheap to refuse).
	if epoch, err := k.Epoch.Get(ctx); err != nil {
		return math.Int{}, err
	} else if r, err := k.UnbondRecords.Get(ctx, collections.Join(m.Validator, epoch.Number)); err == nil && r.Status != types.UNBOND_STATUS_PENDING {
		return math.Int{}, types.ErrNotMatured.Wrapf("%s/%d is already %s", m.Validator, epoch.Number, r.Status)
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return math.Int{}, err
	}
	d := math.NewIntFromUint64(m.Amount)
	if d.GT(s) {
		return math.Int{}, errorsmod.Wrap(types.ErrAmount, "more derth than exists")
	}
	u := valueOf(d, b, s)
	if err := fitsNote(u); err != nil {
		return math.Int{}, err
	}
	return u, nil
}

// checkClaim returns the record and the ERTH the claim pays.
func (k Keeper) checkClaim(ctx context.Context, m *types.MsgClaimUnbonding) (types.UnbondRecord, math.Int, error) {
	if _, err := k.valAddr(m.Validator); err != nil {
		return types.UnbondRecord{}, math.Int{}, err
	}
	r, err := k.UnbondRecords.Get(ctx, collections.Join(m.Validator, m.Epoch))
	if errors.Is(err, collections.ErrNotFound) {
		return r, math.Int{}, types.ErrUnknownRecord.Wrapf("%s/%d", m.Validator, m.Epoch)
	} else if err != nil {
		return r, math.Int{}, err
	}
	if r.Status != types.UNBOND_STATUS_MATURED {
		return r, math.Int{}, types.ErrNotMatured.Wrapf("%s/%d is %s", m.Validator, m.Epoch, r.Status)
	}
	v := math.NewIntFromUint64(m.Amount)
	if v.GT(r.Outstanding) {
		return r, math.Int{}, errorsmod.Wrap(types.ErrAmount, "claim exceeds the record's outstanding notes")
	}
	pay := v.Mul(r.Payout).Quo(r.Requested)
	if m.FeeFromOutput > 0 && !pay.GT(math.NewIntFromUint64(m.FeeFromOutput)) {
		return r, math.Int{}, errorsmod.Wrapf(types.ErrAmount, "claim pays %s%s, not more than its fee %d", pay, types.BondDenom, m.FeeFromOutput)
	}
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return r, math.Int{}, err
	}
	return r, pay, nil
}

// checkStakeVote: the proposal is open to stake votes and the stake proof
// spends against its snapshot stake root (so every note it spends existed
// then, and one minted since, including a vote's own re-mint, cannot vote),
// and the weight fits the validator's snapshot supply.
func (k Keeper) checkStakeVote(ctx context.Context, m *types.MsgStakeVote) (math.Int, error) {
	if _, err := k.valAddr(m.Validator); err != nil {
		return math.Int{}, err
	}
	snap, supply, err := k.openSnapshot(ctx, m.ProposalId, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	if !bytes.Equal(m.Stake.Anchor, snap.Root) {
		return math.Int{}, types.ErrNoVoting.Wrapf("a stake vote on proposal %d spends against its snapshot root", m.ProposalId)
	}
	d := math.NewIntFromUint64(m.Weight)
	if d.GT(supply) {
		return math.Int{}, errorsmod.Wrap(types.ErrAmount, "vote exceeds the validator's derth supply at the snapshot")
	}
	return d, nil
}

func (k Keeper) checkLock(ctx context.Context, m *types.MsgLockPosition) error {
	if _, err := k.valAddr(m.Validator); err != nil {
		return err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if math.NewIntFromUint64(m.Amount).LT(params.MinPosition) {
		return types.ErrPosition.Wrapf("a position locks at least %s derth", params.MinPosition)
	}
	if err := k.allocation.ValidateSplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, m.Splits); err != nil {
		return err
	}
	if len(m.Splits) > 0 && !k.positionWeight(ctx, m.Validator, math.NewIntFromUint64(m.Amount)).IsPositive() {
		return allocationtypes.ErrNoWeight
	}
	return nil
}

// checkUpdate: the owner's proof, a valid split, and (as LockPosition
// requires) a split only on a position that has weight (audit F9).
func (k Keeper) checkUpdate(ctx context.Context, m *types.MsgUpdatePosition) (types.Position, error) {
	p, err := k.checkPositionOwner(ctx, m.PositionId, &m.Stake)
	if err != nil {
		return p, err
	}
	if err := k.allocation.ValidateSplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, m.Splits); err != nil {
		return p, err
	}
	if len(m.Splits) > 0 && !k.positionWeight(ctx, p.Validator, p.Derth).IsPositive() {
		return p, allocationtypes.ErrNoWeight
	}
	return p, nil
}

// checkPositionOwner returns the position if the stake proof's owner tag is
// the one it stores: the proof (verified by the ante) shows its prover owns
// that tag.
func (k Keeper) checkPositionOwner(ctx context.Context, id uint64, sp *types.StakeProof) (types.Position, error) {
	p, err := k.Positions.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return p, types.ErrPosition.Wrapf("no position %d", id)
	} else if err != nil {
		return p, err
	}
	if !bytes.Equal(p.OwnerTag, sp.OwnerTag) {
		return p, types.ErrSignature.Wrapf("position %d", id)
	}
	return p, nil
}

func (k Keeper) checkPositionVote(ctx context.Context, m *types.MsgPositionVote) (types.Position, math.Int, error) {
	p, err := k.checkPositionOwner(ctx, m.PositionId, &m.Stake)
	if err != nil {
		return p, math.Int{}, err
	}
	snap, vs, err := k.openSnapshot(ctx, m.ProposalId, p.Validator)
	if err != nil {
		return p, vs, err
	}
	// A position locked at or after the snapshot's block holds derth whose
	// notes could still vote from the snapshot root; it may not vote too.
	if p.CreatedHeight >= snap.Height {
		return p, vs, types.ErrNoVoting.Wrapf("position %d was created after proposal %d entered voting", p.Id, m.ProposalId)
	}
	return p, vs, nil
}
