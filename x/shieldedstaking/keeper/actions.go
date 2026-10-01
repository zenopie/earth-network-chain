package keeper

import (
	"bytes"
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Private actions: what the x/shielded ante runs for this module's msgs
// before it spends their transfer (see x/shielded/types.PrivateActionHandler).
//
// Check refuses everything the msg's handler would refuse, because the ante's
// spend stands even when the handler fails: a refused Delegate after the ante
// released its ERTH would leave that ERTH in the pool with no note for it.
// The handler repeats the same checks on the same state (both run inside one
// DeliverTx), so it cannot disagree.
//
// Gas is fixed per msg type and prepaid with the transfer's, and handlers run
// their effects on an infinite meter, as x/shielded's MsgTransfer does. An
// unsigned tx's gas limit can be rewritten by whoever relays it; with metered
// effects they could pick a limit that passes the ante and runs out in the
// handler, after the notes are spent.

// Base gas per action, on top of the transfer's and of one note write per
// note the action mints. Covers the handler's reads and writes (the rate's
// reward computation is the heaviest: a distribution period walk).
const (
	gasDelegate   uint64 = 400_000
	gasUndelegate uint64 = 400_000
	gasClaim      uint64 = 250_000
	gasVote       uint64 = 250_000
	gasLock       uint64 = 400_000
	gasUpdate     uint64 = 300_000
	gasUnlock     uint64 = 300_000
	gasPosVote    uint64 = 250_000
)

// prepared marks a msg whose action the ante checked. Handlers recompute
// what they need.
type prepared struct{ kind string }

// ActionHandler implements x/shielded's PrivateActionHandler for every
// private msg of this module.
type ActionHandler struct{ k Keeper }

// NewActionHandler returns the handler registered for this module's msgs.
func NewActionHandler(k Keeper) ActionHandler { return ActionHandler{k: k} }

// RegisterPrivateActions registers h for each of this module's private msgs.
func RegisterPrivateActions(register func(string, shieldedtypes.PrivateActionHandler), h ActionHandler) {
	for _, t := range []string{
		types.TypeMsgDelegate, types.TypeMsgUndelegate, types.TypeMsgClaimUnbonding, types.TypeMsgStakeVote,
		types.TypeMsgLockPosition, types.TypeMsgUpdatePosition, types.TypeMsgUnlockPosition, types.TypeMsgPositionVote,
	} {
		register(t, h)
	}
}

func (h ActionHandler) PrivateActionGas(ctx context.Context, msg shieldedtypes.PrivateMsg) (uint64, error) {
	_, note, err := h.k.shielded.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	switch msg.(type) {
	case *types.MsgDelegate:
		return gasDelegate + note, nil
	case *types.MsgUndelegate:
		return gasUndelegate + note, nil
	case *types.MsgClaimUnbonding:
		return gasClaim + note, nil
	case *types.MsgStakeVote:
		return gasVote + note, nil
	case *types.MsgLockPosition:
		return gasLock, nil
	case *types.MsgUpdatePosition:
		return gasUpdate, nil
	case *types.MsgUnlockPosition:
		return gasUnlock + note, nil
	case *types.MsgPositionVote:
		return gasPosVote, nil
	}
	return 0, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
}

func (h ActionHandler) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	k := h.k
	var err error
	switch m := msg.(type) {
	case *types.MsgDelegate:
		_, err = k.checkDelegate(ctx, m)
	case *types.MsgUndelegate:
		_, err = k.checkUndelegate(ctx, m)
	case *types.MsgClaimUnbonding:
		_, _, err = k.checkClaim(ctx, m)
	case *types.MsgStakeVote:
		_, err = k.checkStakeVote(ctx, m)
	case *types.MsgLockPosition:
		err = k.checkLock(ctx, m)
	case *types.MsgUpdatePosition:
		_, err = k.checkPositionSig(ctx, m.PositionId, "update", m.SignPayload(), m.Signature)
		if err == nil {
			err = k.allocation.ValidateSplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, m.Splits)
		}
	case *types.MsgUnlockPosition:
		if _, err = k.checkPositionSig(ctx, m.PositionId, "unlock", m.SignPayload(), m.Signature); err == nil {
			err = k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext)
		}
	case *types.MsgPositionVote:
		_, _, err = k.checkPositionVote(ctx, m)
	default:
		err = errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	if err != nil {
		return nil, err
	}
	return prepared{kind: sdk.MsgTypeURL(msg)}, nil
}

// VerifyPrivateAction: no msg of this module carries a proof beyond its
// transfer.
func (h ActionHandler) VerifyPrivateAction(context.Context, shieldedtypes.PrivateMsg, any) error {
	return nil
}

// AcceptsPrivateAnchor lets a stake vote spend against its proposal's
// snapshot root after that root has left the pool's anchor window (a voting
// period can outlast it). Nothing else is accepted outside the window.
func (h ActionHandler) AcceptsPrivateAnchor(ctx context.Context, msg shieldedtypes.PrivateMsg, root []byte) (bool, error) {
	m, ok := msg.(*types.MsgStakeVote)
	if !ok {
		return false, nil
	}
	snap, _, err := h.k.openSnapshot(ctx, m.ProposalId, m.Validator)
	if err != nil {
		return false, nil
	}
	return bytes.Equal(snap.Root, root), nil
}

// authorized is the handlers' gate: the ante checked this very msg's action
// and executed its transfer in this tx. Returns ctx on an infinite meter.
func (k Keeper) authorized(ctx context.Context, msg shieldedtypes.PrivateMsg) (sdk.Context, error) {
	if _, err := shieldedkeeper.AuthorizedAction(ctx, msg.PrivateTransfer()); err != nil {
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
	b, s, err := k.Backing(ctx, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	d, err := derthFor(math.NewIntFromUint64(m.Transfer.ValueOut), b, s)
	if err != nil {
		return math.Int{}, err
	}
	if err := fitsNote(d); err != nil {
		return math.Int{}, err
	}
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return math.Int{}, err
	}
	return d, nil
}

// checkUndelegate returns the unbond note's value.
func (k Keeper) checkUndelegate(ctx context.Context, m *types.MsgUndelegate) (math.Int, error) {
	if _, err := k.valAddr(m.Validator); err != nil {
		return math.Int{}, err
	}
	b, s, err := k.Backing(ctx, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	d := math.NewIntFromUint64(m.Transfer.ValueOut)
	if d.GT(s) {
		return math.Int{}, errorsmod.Wrap(types.ErrAmount, "more derth than exists")
	}
	u := valueOf(d, b, s)
	if err := fitsNote(u); err != nil {
		return math.Int{}, err
	}
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return math.Int{}, err
	}
	return u, nil
}

// checkClaim returns the record and the ERTH the claim pays.
func (k Keeper) checkClaim(ctx context.Context, m *types.MsgClaimUnbonding) (types.UnbondRecord, math.Int, error) {
	r, err := k.UnbondRecords.Get(ctx, collections.Join(m.Validator, m.Epoch))
	if errors.Is(err, collections.ErrNotFound) {
		return r, math.Int{}, types.ErrUnknownRecord.Wrapf("%s/%d", m.Validator, m.Epoch)
	} else if err != nil {
		return r, math.Int{}, err
	}
	if r.Status != types.UNBOND_STATUS_MATURED {
		return r, math.Int{}, types.ErrNotMatured.Wrapf("%s/%d is %s", m.Validator, m.Epoch, r.Status)
	}
	v := math.NewIntFromUint64(m.Transfer.ValueOut)
	if v.GT(r.Outstanding) {
		return r, math.Int{}, errorsmod.Wrap(types.ErrAmount, "claim exceeds the record's outstanding notes")
	}
	pay := v.Mul(r.Payout).Quo(r.Requested)
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return r, math.Int{}, err
	}
	return r, pay, nil
}

// checkStakeVote: the proposal is open to stake votes, the transfer spends
// against its snapshot root (so every note it spends existed then, and one
// minted since, including a vote's own re-minted note, cannot vote), its
// weight fits the validator's snapshot supply, and the note can be minted
// back. The transfer's nullifiers are checked unspent by the ante.
func (k Keeper) checkStakeVote(ctx context.Context, m *types.MsgStakeVote) (math.Int, error) {
	snap, vs, err := k.openSnapshot(ctx, m.ProposalId, m.Validator)
	if err != nil {
		return math.Int{}, err
	}
	if !bytes.Equal(m.Transfer.Root, snap.Root) {
		return math.Int{}, types.ErrNoVoting.Wrapf("a stake vote on proposal %d spends against its snapshot root", m.ProposalId)
	}
	d := math.NewIntFromUint64(m.Transfer.ValueOut)
	if d.GT(vs.Supply) {
		return math.Int{}, errorsmod.Wrap(types.ErrAmount, "vote exceeds the validator's derth supply at the snapshot")
	}
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return math.Int{}, err
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
	if math.NewIntFromUint64(m.Transfer.ValueOut).LT(params.MinPosition) {
		return types.ErrPosition.Wrapf("a position locks at least %s derth", params.MinPosition)
	}
	n, err := k.positionCount(ctx)
	if err != nil {
		return err
	}
	if n >= params.MaxPositions {
		return types.ErrPosition.Wrapf("%d positions exist already", n)
	}
	if err := k.allocation.ValidateSplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, m.Splits); err != nil {
		return err
	}
	if len(m.Splits) > 0 && !k.positionWeight(ctx, m.Validator, math.NewIntFromUint64(m.Transfer.ValueOut)).IsPositive() {
		return allocationtypes.ErrNoWeight
	}
	return nil
}

// checkPositionSig returns the position once its key's signature over
// action, the position's current nonce and payload verifies.
func (k Keeper) checkPositionSig(ctx context.Context, id uint64, action string, payload, sig []byte) (types.Position, error) {
	p, err := k.Positions.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return p, types.ErrPosition.Wrapf("no position %d", id)
	} else if err != nil {
		return p, err
	}
	pk := secp256k1.PubKey{Key: p.Pubkey}
	msg := types.PositionSignBytes(sdk.UnwrapSDKContext(ctx).ChainID(), action, id, p.Nonce, payload)
	if !pk.VerifySignature(msg, sig) {
		return p, types.ErrSignature
	}
	return p, nil
}

func (k Keeper) checkPositionVote(ctx context.Context, m *types.MsgPositionVote) (types.Position, types.ValidatorSnapshot, error) {
	p, err := k.checkPositionSig(ctx, m.PositionId, "vote", m.SignPayload(), m.Signature)
	if err != nil {
		return p, types.ValidatorSnapshot{}, err
	}
	snap, vs, err := k.openSnapshot(ctx, m.ProposalId, p.Validator)
	if err != nil {
		return p, vs, err
	}
	// A position locked at or after the snapshot's block holds derth whose
	// note could still vote from the snapshot root; it may not vote too.
	if p.CreatedHeight >= snap.Height {
		return p, vs, types.ErrNoVoting.Wrapf("position %d was created after proposal %d entered voting", p.Id, m.ProposalId)
	}
	return p, vs, nil
}
