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
// note tree, except MsgStakeVote, which carries a vote proof (circuits/vote)
// against its proposal's snapshot; the ante runs Check (state: the proof's
// shape for the msg, its nullifiers unspent, its anchor, the msg's own
// checks) and Verify (the proof, against the msg's sighash) before it spends
// the fee bundle.
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
// and two note writes per nullifier slot (an indexed-tree insert) and one per
// output slot. Covers
// the handler's reads and writes (the rate's reward computation is the
// heaviest: a distribution period walk).
const (
	gasDelegate   uint64 = 400_000
	gasRestake    uint64 = 100_000
	gasUndelegate uint64 = 400_000
	gasVote       uint64 = 250_000
	gasLock       uint64 = 400_000
	gasUpdate     uint64 = 300_000
	gasUnlock     uint64 = 300_000
	gasPosVote    uint64 = 250_000
)

// preparedVote is what Check derived for a stake vote: the sighash and the
// snapshot roots the vote proof is verified against.
type preparedVote struct {
	sighash          fr.Element
	noteRoot, nfRoot []byte
}

// prepared is what Check derived: the sighash the stake proof binds and
// what the chain supplies to it (StakeLanes). Handlers recompute what else
// they need.
type prepared struct {
	sighash fr.Element
	lanes   types.StakeLanes
}

// ActionHandler implements x/shielded's PrivateActionHandler for every
// private msg of this module.
type ActionHandler struct{ k Keeper }

// NewActionHandler returns the handler registered for this module's msgs.
func NewActionHandler(k Keeper) ActionHandler { return ActionHandler{k: k} }

// RegisterPrivateActions registers h for each of this module's private msgs.
func RegisterPrivateActions(register func(string, shieldedtypes.PrivateActionHandler), h ActionHandler) {
	for _, t := range []string{
		types.TypeMsgDelegate, types.TypeMsgRestake, types.TypeMsgUndelegate,
		types.TypeMsgStakeVote, types.TypeMsgLockPosition, types.TypeMsgUpdatePosition, types.TypeMsgUnlockPosition,
		types.TypeMsgPositionVote, types.TypeMsgRedelegate,
	} {
		register(t, h)
	}
}

// ReleasedDenoms: a delegation takes its uerth into the module; every other
// staking msg only pays a fee (its value moves in the stake tree).
func (h ActionHandler) ReleasedDenoms(msg shieldedtypes.PrivateMsg) []string {
	if _, ok := msg.(*types.MsgDelegate); ok {
		return []string{types.BondDenom}
	}
	return nil
}

func (h ActionHandler) PrivateActionGas(ctx context.Context, msg shieldedtypes.PrivateMsg) (uint64, error) {
	proof, note, err := h.k.shielded.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	// A stake vote: the proof, one write for the vote and one per vote
	// nullifier (always MaxVoteNotes: padding included), whatever the tree
	// sizes.
	if m, ok := msg.(*types.MsgStakeVote); ok {
		return gasVote + proof + uint64(1+len(m.VoteNullifiers))*note, nil
	}
	sm, ok := msg.(types.StakeMsg)
	if !ok {
		return 0, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	// The proof, two writes per nullifier slot (an insert into the indexed
	// nullifier tree rewrites two paths: the low leaf's and the new leaf's)
	// and one per output slot, whether used or not: lane A's two nullifiers
	// and one output, and the credit lane's one and one for a msg crediting
	// a second asset. An undelegation adds one for its queued payout (which
	// mints its pool note later, for free).
	nfSlots, cmSlots := uint64(2), uint64(1)
	if sm.StakeLanes().CreditDenom != "" {
		nfSlots, cmSlots = nfSlots+1, cmSlots+1
	}
	writes := 2*nfSlots + cmSlots
	if _, ok := msg.(*types.MsgUndelegate); ok {
		writes++
	}
	var base uint64
	switch msg.(type) {
	case *types.MsgDelegate:
		base = gasDelegate
	case *types.MsgRestake:
		base = gasRestake
	case *types.MsgUndelegate:
		base = gasUndelegate
	case *types.MsgLockPosition:
		base = gasLock
	case *types.MsgUpdatePosition:
		base = gasUpdate
	case *types.MsgUnlockPosition:
		base = gasUnlock
	case *types.MsgPositionVote:
		base = gasPosVote
	case *types.MsgRedelegate:
		g, err := h.k.redelegateGas(ctx, msg.(*types.MsgRedelegate))
		if err != nil {
			return 0, err
		}
		base = g
	default:
		return 0, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	return base + proof + writes*note, nil
}

func (h ActionHandler) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	k := h.k
	if m, ok := msg.(*types.MsgStakeVote); ok {
		snap, _, err := k.checkStakeVote(ctx, m)
		if err != nil {
			return nil, err
		}
		sighash, err := shieldedtypes.SighashOf(ctx, msg, k.addressCodec)
		if err != nil {
			return nil, err
		}
		return preparedVote{sighash: sighash, noteRoot: snap.Root, nfRoot: snap.NfRoot}, nil
	}
	sm, ok := msg.(types.StakeMsg)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidMsg, "no private action for %T", msg)
	}
	var err error
	lanes := sm.StakeLanes()
	switch m := msg.(type) {
	case *types.MsgDelegate:
		err = k.checkDelegate(ctx, m)
	case *types.MsgRestake:
		_, err = k.valAddr(m.Validator)
	case *types.MsgUndelegate:
		_, err = k.checkUndelegate(ctx, m)
	case *types.MsgLockPosition:
		err = k.checkLock(ctx, m)
	case *types.MsgUpdatePosition:
		_, err = k.checkUpdate(ctx, m)
	case *types.MsgUnlockPosition:
		var p types.Position
		if p, err = k.checkPositionOwner(ctx, m.PositionId, &m.Stake); err == nil {
			lanes = types.UnlockLanes(p)
		}
	case *types.MsgPositionVote:
		_, _, err = k.checkPositionVote(ctx, m)
	case *types.MsgRedelegate:
		_, err = k.checkRedelegate(ctx, m)
	}
	if err != nil {
		return nil, err
	}
	p := sm.StakeProofOf()
	if err := k.checkStakeNullifiers(ctx, p.SpentNullifiers()); err != nil {
		return nil, err
	}
	// A proof that may clear a slash label names an allowed clear_before
	// and reads the current debt root (moves.go).
	if err := k.checkStakeClear(ctx, p); err != nil {
		return nil, err
	}
	// Every proof that publishes a nullifier proves against the window,
	// padding included (the chain cannot tell it from a real spend). A proof
	// that spends nothing proves no membership.
	if len(p.SpentNullifiers()) > 0 {
		if err := k.checkStakeAnchor(ctx, p.Anchor); err != nil {
			return nil, err
		}
	}
	cms, _ := p.Outputs()
	if err := k.checkStakeCapacity(ctx, uint64(len(cms))); err != nil {
		return nil, err
	}
	sighash, err := shieldedtypes.SighashOf(ctx, msg, k.addressCodec)
	if err != nil {
		return nil, err
	}
	return prepared{sighash: sighash, lanes: lanes}, nil
}

// VerifyPrivateAction verifies the stake proof (a vote's vote proof, against
// its proposal's snapshot roots) against the msg's sighash.
func (h ActionHandler) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, pr any) error {
	if m, ok := msg.(*types.MsgStakeVote); ok {
		p := pr.(preparedVote)
		if err := h.k.shielded.VerifyCircuit(ctx, shieldedtypes.CircuitVote, m.Proof, m.VotePublicInputs(p.noteRoot, p.nfRoot, p.sighash)); err != nil {
			return errorsmod.Wrap(types.ErrInvalidStakeProof, err.Error())
		}
		return nil
	}
	sm := msg.(types.StakeMsg)
	p := pr.(prepared)
	sp := sm.StakeProofOf()
	if err := h.k.shielded.VerifyCircuit(ctx, shieldedtypes.CircuitStake, sp.Proof, sp.PublicInputs(p.lanes, p.sighash)); err != nil {
		return errorsmod.Wrap(types.ErrInvalidStakeProof, err.Error())
	}
	return nil
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

// fitsNote refuses a value the chain would not mint as one note:
// 1..shieldedtypes.MaxNoteValue (2^63-1, what every wallet holds).
func fitsNote(v math.Int) error {
	if !shieldedtypes.FitsNote(v) {
		return errorsmod.Wrapf(types.ErrAmount, "%s", v)
	}
	return nil
}

// checkDelegate refuses a delegation its amount does not pay for: the derth
// it credits must be at most what amount buys at the live rate.
func (k Keeper) checkDelegate(ctx context.Context, m *types.MsgDelegate) error {
	if err := k.checkDelegatable(ctx, m.Validator); err != nil {
		return err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	amount := math.NewIntFromUint64(m.Delegated())
	if amount.LT(params.MinDelegation) {
		return errorsmod.Wrapf(types.ErrAmount, "a delegation is at least %s%s", params.MinDelegation, types.BondDenom)
	}
	b, s, err := k.Backing(ctx, m.Validator)
	if err != nil {
		return err
	}
	if !s.IsPositive() && b.IsPositive() {
		// Backing nobody owns (rewards accrued after the last holder's
		// derth was credited): the epoch end settles it (processValidator).
		// A delegation now would buy it at rate 1 (audit F5).
		return types.ErrValidator.Wrapf("%s's book is settling (no derth, %s%s backing); delegate after the epoch end", m.Validator, b, types.BondDenom)
	}
	buys, err := derthFor(amount, b, s)
	if err != nil {
		return err
	}
	return checkCredit("delegation", math.NewIntFromUint64(m.Derth), buys, params.MinDelegation, rateOf(b, s))
}

// checkCredit refuses derth a msg credits to its owner's note (credit) that
// the value it brings does not buy at the live rate (buys, floored), or below
// min_delegation: a delegation's rounding loss (under one derth's value) is
// then at most 1/min_delegation of it, however far a donation to the
// validator's rewards pool has pushed the rate (audit F4). The wallet names
// the derth when it proves (the merged note's amount is in the proof); what
// the value buys beyond it stays in the validator's book. Shared by every
// msg that brings value in at a rate (a delegation, a redelegation's
// arrival).
func checkCredit(what string, credit, buys, min math.Int, rate math.LegacyDec) error {
	if buys.LT(min) {
		return errorsmod.Wrapf(types.ErrAmount, "the %s buys %s derth at the live rate %s, less than the minimum %s", what, buys, rate, min)
	}
	if credit.LT(min) {
		return errorsmod.Wrapf(types.ErrAmount, "the %s credits %s derth, less than the minimum %s", what, credit, min)
	}
	if credit.GT(buys) {
		return errorsmod.Wrapf(types.ErrAmount, "the %s buys %s derth at the live rate %s, less than the %s it credits (the rate moved since the proof: re-quote with a margin)",
			what, buys, rate, credit)
	}
	return fitsNote(credit)
}

// checkUndelegate returns the undelegation's ERTH value.
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
	// At most one note's worth when it starts (a slash only lowers the
	// payout; MintNoteSplit would pay more as several notes anyway).
	if err := fitsNote(u); err != nil {
		return math.Int{}, err
	}
	// The payout's pc and ciphertext, and room in the pool's tree (checked
	// again when it is paid).
	if err := k.shielded.CheckMint(ctx, m.Pc, m.Ciphertext); err != nil {
		return math.Int{}, err
	}
	return u, nil
}

// checkStakeVote: the proposal is open to stake votes with both snapshot
// roots, the weight fits the validator's snapshot supply, and the vote
// nullifier has not voted on it. The vote proof (verified by the ante
// against the snapshot's roots) shows the note was in the stake tree and
// unspent when voting began: one minted since, or spent before, cannot vote;
// one spent since still can (its later spend is not in nf_root).
func (k Keeper) checkStakeVote(ctx context.Context, m *types.MsgStakeVote) (types.ProposalSnapshot, math.Int, error) {
	if _, err := k.valAddr(m.Validator); err != nil {
		return types.ProposalSnapshot{}, math.Int{}, err
	}
	snap, supply, err := k.openSnapshot(ctx, m.ProposalId, m.Validator)
	if err != nil {
		return snap, math.Int{}, err
	}
	if len(snap.NfRoot) == 0 {
		return snap, math.Int{}, types.ErrNoVoting.Wrapf("proposal %d's snapshot has no stake nullifier root", m.ProposalId)
	}
	// A labelled note votes its value under the CURRENT debt tree (a slash
	// since the snapshot counts).
	if err := k.checkDebtRoot(ctx, m.DebtRoot); err != nil {
		return snap, math.Int{}, err
	}
	d := math.NewIntFromUint64(m.Weight)
	if d.GT(supply) {
		return snap, math.Int{}, errorsmod.Wrap(types.ErrAmount, "vote exceeds the validator's derth supply at the snapshot")
	}
	for _, vnf := range m.VoteNullifiers {
		if used, err := k.UsedVoteNullifiers.Has(ctx, collections.Join(m.ProposalId, vnf)); err != nil {
			return snap, math.Int{}, err
		} else if used {
			return snap, math.Int{}, types.ErrVoteNullifierUsed.Wrapf("proposal %d, vote nullifier %X", m.ProposalId, vnf)
		}
	}
	return snap, d, nil
}

// noteVoteKey is a note vote's key under its proposal: 0x00 || vote nullifier
// (position votes are 0x01 || id).
func noteVoteKey(vnf []byte) []byte { return append([]byte{0}, vnf...) }

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
	// v_out is a public u64, so two maximal notes could lock 2^64-2: a
	// position that could never unlock (its note would not fit) and that
	// genesis refuses (audit 6 C-L2).
	if err := fitsNote(math.NewIntFromUint64(m.Amount)); err != nil {
		return err
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
