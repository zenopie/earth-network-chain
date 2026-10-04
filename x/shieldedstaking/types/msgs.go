package types

import (
	"bytes"
	"encoding/binary"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Msg type URLs: the kind every sighash binds first.
const (
	TypeMsgDelegate       = "/earth.shieldedstaking.v1.MsgDelegate"
	TypeMsgRestake        = "/earth.shieldedstaking.v1.MsgRestake"
	TypeMsgUndelegate     = "/earth.shieldedstaking.v1.MsgUndelegate"
	TypeMsgStakeVote      = "/earth.shieldedstaking.v1.MsgStakeVote"
	TypeMsgLockPosition   = "/earth.shieldedstaking.v1.MsgLockPosition"
	TypeMsgUpdatePosition = "/earth.shieldedstaking.v1.MsgUpdatePosition"
	TypeMsgUnlockPosition = "/earth.shieldedstaking.v1.MsgUnlockPosition"
	TypeMsgPositionVote   = "/earth.shieldedstaking.v1.MsgPositionVote"
	TypeMsgRedelegate     = "/earth.shieldedstaking.v1.MsgRedelegate"
)

var (
	_ shieldedtypes.PrivateMsg = (*MsgDelegate)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgRestake)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUndelegate)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgStakeVote)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgLockPosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUpdatePosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUnlockPosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgPositionVote)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgRedelegate)(nil)
)

// ---- field encodings the sighashes and position signatures bind ----------
//
// Wallets must reproduce these byte for byte.

// SplitsBytes is 16 bytes per entry: option_id then percent, big-endian u64.
func SplitsBytes(splits []allocationtypes.AllocationWeight) []byte {
	out := make([]byte, 0, 16*len(splits))
	for _, w := range splits {
		out = binary.BigEndian.AppendUint64(out, w.OptionId)
		out = binary.BigEndian.AppendUint64(out, w.Percent)
	}
	return out
}

// OptionsBytes is, per option: the option as a big-endian u64, then the
// weight's canonical decimal string (math.LegacyDec.String, 18 places)
// prefixed by its length as a big-endian u32. Call after ValidateOptions.
func OptionsBytes(opts []*v1.WeightedVoteOption) []byte {
	var out []byte
	for _, o := range opts {
		w, _ := math.LegacyNewDecFromStr(o.Weight)
		ws := w.String()
		out = binary.BigEndian.AppendUint64(out, uint64(o.Option))
		out = binary.BigEndian.AppendUint32(out, uint32(len(ws)))
		out = append(out, ws...)
	}
	return out
}

// ValidateOptions checks a weighted vote: 1..4 distinct valid options, each
// weight in (0, 1], summing to exactly 1.
func ValidateOptions(opts []*v1.WeightedVoteOption) error {
	if len(opts) == 0 || len(opts) > MaxOptionsPerVote {
		return errorsmod.Wrapf(ErrInvalidMsg, "need 1..%d vote options", MaxOptionsPerVote)
	}
	seen := map[v1.VoteOption]bool{}
	sum := math.LegacyZeroDec()
	for _, o := range opts {
		if o == nil || !v1.ValidVoteOption(o.Option) {
			return errorsmod.Wrap(ErrInvalidMsg, "invalid vote option")
		}
		if seen[o.Option] {
			return errorsmod.Wrap(ErrInvalidMsg, "duplicate vote option")
		}
		seen[o.Option] = true
		w, err := math.LegacyNewDecFromStr(o.Weight)
		if err != nil || !w.IsPositive() || w.GT(math.LegacyOneDec()) {
			return errorsmod.Wrapf(ErrInvalidMsg, "invalid vote weight %q", o.Weight)
		}
		// Only the canonical spelling (LegacyDec.String(): 18 decimals) is
		// accepted. OptionsBytes binds the canonical form, so any other
		// spelling would let a relayer re-encode a vote under a new tx hash
		// without touching its proofs.
		if w.String() != o.Weight {
			return errorsmod.Wrapf(ErrInvalidMsg, "vote weight %q is not canonical (want %q)", o.Weight, w.String())
		}
		sum = sum.Add(w)
	}
	if !sum.Equal(math.LegacyOneDec()) {
		return errorsmod.Wrap(ErrInvalidMsg, "vote weights must sum to 1")
	}
	return nil
}

// GovOptions converts to x/gov's type.
func GovOptions(opts []*v1.WeightedVoteOption) v1.WeightedVoteOptions {
	out := make(v1.WeightedVoteOptions, len(opts))
	copy(out, opts)
	return out
}

func field(name string, b []byte) (fr.Element, error) {
	e, err := privacy.FieldFromBytes(b)
	if err != nil {
		return e, errorsmod.Wrapf(ErrInvalidMsg, "%s: %v", name, err)
	}
	return e, nil
}

// checkNoteOut checks a pool note the chain will mint: a pc and its
// amount-blind ciphertext.
func checkNoteOut(pc, ct []byte) error {
	if _, err := field("pc", pc); err != nil {
		return err
	}
	if err := shieldedtypes.CheckBlindCiphertext("ciphertext", ct); err != nil {
		return errorsmod.Wrap(ErrInvalidMsg, err.Error())
	}
	return nil
}

// checkMoves checks msg's release map: a positive fee (unless outputFee pays
// it), and exactly denom released beyond it (a positive amount), or nothing
// when denom is "". Under the fee rule (shieldedtypes.FeeAfter) a msg moving
// no uerth pays its whole uerth balance as the fee.
func checkMoves(msg shieldedtypes.PrivateMsg, denom string, outputFee uint64) error {
	if err := shieldedtypes.ValidateBundles(msg); err != nil {
		return err
	}
	if (msg.PrivateFee() == 0) == (outputFee == 0) {
		return errorsmod.Wrap(ErrInvalidMsg, "the fee is paid by the bundle or from the output, exactly one")
	}
	rem, err := shieldedtypes.Remainders(msg)
	if err != nil {
		return err
	}
	if denom == "" {
		if len(rem) != 0 {
			return errorsmod.Wrap(ErrInvalidMsg, "this msg moves no value: the bundle's only balance is its uerth fee")
		}
		return nil
	}
	if len(rem) != 1 || rem[0].Denom != denom {
		return errorsmod.Wrapf(ErrInvalidMsg, "the bundle must release a positive amount of %s and nothing else beyond its fee", denom)
	}
	return nil
}

// released is what msg releases of denom beyond its fee (0 if nothing).
func released(msg shieldedtypes.PrivateMsg, denom string) uint64 {
	rem, err := shieldedtypes.Remainders(msg)
	if err != nil {
		return 0
	}
	for _, r := range rem {
		if r.Denom == denom {
			return r.Amount
		}
	}
	return 0
}

func checkValidator(v string) error { return CanonicalValoper(v) }

func bundle(b *shieldedtypes.Bundle) []*shieldedtypes.Bundle { return []*shieldedtypes.Bundle{b} }

// ---- the stake proof -------------------------------------------------------

// StakeProofInputs is the stake circuit's public input count.
const StakeProofInputs = 16

var zero32 = make([]byte, 32)

func isZero(b []byte) bool { return bytes.Equal(b, zero32) }

// StakeAsset is a stake denom's asset id (zk/privacy.AssetID): the stake
// circuit's public asset. 0 for no stake denom (a position's update or vote,
// whose proof spends and creates nothing; the credit lane of a msg crediting
// no second asset).
func StakeAsset(denom string) fr.Element {
	if denom == "" {
		return fr.Element{}
	}
	return privacy.AssetID(denom)
}

// StakeLanes is what the chain supplies to a stake proof for a msg: lane A's
// denom, the derth it credits (v_in) and the derth leaving (v_out); the
// credit lane's denom, the derth it credits there (cr_v_in) and the move
// time labelling it (cr_move_time), "", 0 and 0 when the msg credits no
// second asset.
type StakeLanes struct {
	Denom          string
	VIn, VOut      uint64
	CreditDenom    string
	CreditIn       uint64
	CreditMoveTime uint64
}

// ValidateBasic checks a stake proof's form: a proof, every field a canonical
// 32-byte element, exactly two lane A nullifiers, the non-zero nullifiers
// distinct, and a 153-byte wallet stake ciphertext exactly for each non-zero
// commitment. Which slots a msg must use is shape's.
func (p *StakeProof) ValidateBasic() error {
	if err := shieldedtypes.CheckProofLength(p.Proof); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "stake proof: %v", err)
	}
	for _, f := range []struct {
		name string
		b    []byte
	}{
		{"stake anchor", p.Anchor}, {"stake owner_tag", p.OwnerTag}, {"stake commitment", p.Commitment},
		{"stake credit_nullifier", p.CreditNullifier}, {"stake credit_commitment", p.CreditCommitment},
		{"stake debt_root", p.DebtRoot},
	} {
		if _, err := field(f.name, f.b); err != nil {
			return err
		}
	}
	if len(p.Nullifiers) != 2 {
		return errorsmod.Wrap(ErrInvalidMsg, "a stake proof carries exactly two nullifiers")
	}
	for _, nf := range p.Nullifiers {
		if _, err := field("stake nullifier", nf); err != nil {
			return err
		}
	}
	// A proof that clears nothing (clear_before 0) reads no debt root: one
	// encoding, the zero root.
	if p.ClearBefore == 0 && !isZero(p.DebtRoot) {
		return errorsmod.Wrap(ErrInvalidMsg, "debt_root is zero when clear_before is 0 (the proof clears no label)")
	}
	seen := map[string]bool{}
	for _, nf := range p.SpentNullifiers() {
		if seen[string(nf)] {
			return errorsmod.Wrap(ErrInvalidMsg, "duplicate stake nullifier")
		}
		seen[string(nf)] = true
	}
	// One encoding per msg: a ciphertext exactly for a created note.
	for _, o := range []struct {
		name   string
		cm, ct []byte
	}{{"ciphertext", p.Commitment, p.Ciphertext}, {"credit_ciphertext", p.CreditCommitment, p.CreditCiphertext}} {
		if isZero(o.cm) != (len(o.ct) == 0) {
			return errorsmod.Wrapf(ErrInvalidMsg, "%s must be present iff its commitment is non-zero", o.name)
		}
		if len(o.ct) != 0 && len(o.ct) != privacy.WalletStakeCiphertextBytes {
			return errorsmod.Wrapf(ErrInvalidMsg, "%s must be exactly %d bytes (wallet stake note), got %d",
				o.name, privacy.WalletStakeCiphertextBytes, len(o.ct))
		}
	}
	return nil
}

// SpentNullifiers is the proof's non-zero nullifiers, in order: lane A's,
// then the credit lane's. Each is spent (inserted); the chain cannot tell a
// padding nullifier from a real one, and need not.
func (p *StakeProof) SpentNullifiers() [][]byte {
	var out [][]byte
	for _, nf := range append(append([][]byte{}, p.Nullifiers...), p.CreditNullifier) {
		if len(nf) != 0 && !isZero(nf) {
			out = append(out, nf)
		}
	}
	return out
}

// Outputs is the proof's non-zero commitments, in append order (lane A's,
// then the credit lane's), each with its ciphertext.
func (p *StakeProof) Outputs() (cms, cts [][]byte) {
	if len(p.Commitment) != 0 && !isZero(p.Commitment) {
		cms, cts = append(cms, p.Commitment), append(cts, p.Ciphertext)
	}
	if len(p.CreditCommitment) != 0 && !isZero(p.CreditCommitment) {
		cms, cts = append(cms, p.CreditCommitment), append(cts, p.CreditCiphertext)
	}
	return cms, cts
}

// shape checks the slots a msg uses, so that every msg of a kind looks the
// same. notes: lane A spends (nf_0 non-zero: the owner's note, or padding;
// nf_1 optional, a second note being merged) and creates (the merged note,
// the change, or a padding zero note); without notes, lane A is all zero (a
// position's update or vote). credit: the credit lane spends (the owner's
// note of the credited asset, or padding) and creates the merged note; else
// it is zero.
func (p *StakeProof) shape(notes, credit bool) error {
	if err := p.ValidateBasic(); err != nil {
		return err
	}
	if notes {
		if isZero(p.Nullifiers[0]) {
			return errorsmod.Wrap(ErrInvalidMsg, "the stake proof spends a note (or pads with its own nullifier) in its first slot")
		}
		if isZero(p.Commitment) {
			return errorsmod.Wrap(ErrInvalidMsg, "the stake proof creates a note (the merged note, the change or a zero note)")
		}
	} else if !isZero(p.Nullifiers[0]) || !isZero(p.Nullifiers[1]) || !isZero(p.Commitment) {
		return errorsmod.Wrap(ErrInvalidMsg, "the stake proof spends and creates nothing for this msg")
	}
	if credit {
		if isZero(p.CreditNullifier) || isZero(p.CreditCommitment) {
			return errorsmod.Wrap(ErrInvalidMsg, "the stake proof's credit lane spends a note (or pads) and creates the merged note")
		}
	} else if !isZero(p.CreditNullifier) || !isZero(p.CreditCommitment) {
		return errorsmod.Wrap(ErrInvalidMsg, "this msg credits no second asset: the credit lane is zero")
	}
	return nil
}

func fieldOrZero(b []byte) fr.Element {
	e, _ := privacy.FieldFromBytes(b)
	return e
}

// StakeFields are the stake proof's values every staking msg's sighash binds
// first: anchor, nf_0, nf_1, cm, Bytes(ct), credit_nf, credit_cm,
// Bytes(credit_ct), owner_tag, clear_before, debt_root (an absent ciphertext
// is Bytes of nothing).
func (p *StakeProof) StakeFields() []fr.Element {
	at := func(xs [][]byte, i int) []byte {
		if i < len(xs) {
			return xs[i]
		}
		return nil
	}
	return []fr.Element{
		fieldOrZero(p.Anchor), fieldOrZero(at(p.Nullifiers, 0)), fieldOrZero(at(p.Nullifiers, 1)),
		fieldOrZero(p.Commitment), privacy.Bytes(p.Ciphertext),
		fieldOrZero(p.CreditNullifier), fieldOrZero(p.CreditCommitment), privacy.Bytes(p.CreditCiphertext),
		fieldOrZero(p.OwnerTag), privacy.U64(p.ClearBefore), fieldOrZero(p.DebtRoot),
	}
}

// PublicInputs lays out the stake circuit's public inputs: anchor, asset,
// nf_0, nf_1, cm_out, v_in, v_out, clear_before, debt_root, cr_asset, cr_nf,
// cr_cm, cr_v_in, cr_move_time, owner_tag, sighash. Call after
// ValidateBasic.
func (p *StakeProof) PublicInputs(l StakeLanes, sighash fr.Element) [][]byte {
	u := func(v uint64) []byte { return privacy.FieldBytes(privacy.U64(v)) }
	return [][]byte{
		p.Anchor, privacy.FieldBytes(StakeAsset(l.Denom)), p.Nullifiers[0], p.Nullifiers[1], p.Commitment,
		u(l.VIn), u(l.VOut), u(p.ClearBefore), p.DebtRoot,
		privacy.FieldBytes(StakeAsset(l.CreditDenom)), p.CreditNullifier, p.CreditCommitment, u(l.CreditIn),
		u(l.CreditMoveTime), p.OwnerTag, privacy.FieldBytes(sighash),
	}
}

// StakeMsg is a staking msg with a stake proof and what the chain supplies
// to it. MsgUnlockPosition's lane A follows its position (the keeper fills
// it in: StakeLanes returns none).
type StakeMsg interface {
	shieldedtypes.PrivateMsg
	StakeProofOf() *StakeProof
	StakeLanes() StakeLanes
}

var (
	_ StakeMsg = (*MsgDelegate)(nil)
	_ StakeMsg = (*MsgRestake)(nil)
	_ StakeMsg = (*MsgUndelegate)(nil)
	_ StakeMsg = (*MsgLockPosition)(nil)
	_ StakeMsg = (*MsgUpdatePosition)(nil)
	_ StakeMsg = (*MsgUnlockPosition)(nil)
	_ StakeMsg = (*MsgPositionVote)(nil)
	_ StakeMsg = (*MsgRedelegate)(nil)
)

func withStake(p *StakeProof, fields ...fr.Element) []fr.Element {
	return append(p.StakeFields(), fields...)
}

// VoteWeightSigFigs is how many significant decimal digits a stake vote's
// weight may have. Credited amounts are public (delegate, unlock and
// redelegate events), and a note's amount is one of them when the note holds
// nothing else, so an exact weight could link a vote to the txs that made
// its note, and the same note's votes across proposals. Every wallet rounds
// the weight down to this many digits, and the chain refuses any other, so
// all weights fall in the same buckets (audit 6 C-L3). The circuit asks only
// 0 < weight <= amount.
const VoteWeightSigFigs = 3

// RoundVoteWeight rounds w down to VoteWeightSigFigs significant digits.
func RoundVoteWeight(w uint64) uint64 {
	scale := uint64(1)
	for w/scale >= 1000 {
		scale *= 10
	}
	return w / scale * scale
}

// CheckVoteWeight refuses a weight with more than VoteWeightSigFigs
// significant digits.
func CheckVoteWeight(w uint64) error {
	if RoundVoteWeight(w) != w {
		return errorsmod.Wrapf(ErrInvalidMsg, "weight %d has more than %d significant digits (round it down: %d)",
			w, VoteWeightSigFigs, RoundVoteWeight(w))
	}
	return nil
}

func positive(what string, v uint64) error {
	if v == 0 {
		return errorsmod.Wrapf(ErrInvalidMsg, "%s must be positive", what)
	}
	return nil
}

// ---- MsgDelegate ----------------------------------------------------------

func (m *MsgDelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgDelegate) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, m.Amount) }
func (m *MsgDelegate) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: derth/<validator>, crediting derth.
func (m *MsgDelegate) StakeLanes() StakeLanes {
	return StakeLanes{Denom: DerthDenom(m.Validator), VIn: m.Derth}
}

// Delegated is the uerth delegated: what the bundle releases beyond the fee
// (amount, once ValidateBasic passed).
func (m *MsgDelegate) Delegated() uint64 { return released(m, BondDenom) }

// SighashFields: StakeFields, Bytes(validator), amount, derth.
func (m *MsgDelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Amount), privacy.U64(m.Derth)), nil
}

// ValidateBasic: a positive amount released by the bundle, a positive derth,
// and a proof merging into the owner's derth/<validator> note.
func (m *MsgDelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := positive("amount", m.Amount); err != nil {
		return err
	}
	if err := positive("derth", m.Derth); err != nil {
		return err
	}
	if err := checkMoves(m, BondDenom, 0); err != nil {
		return err
	}
	if released(m, BondDenom) != m.Amount {
		return errorsmod.Wrap(ErrInvalidMsg, "the bundle must release amount uerth beyond its fee")
	}
	return m.Stake.shape(true, false)
}

// ---- MsgRestake -----------------------------------------------------------

func (m *MsgRestake) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgRestake) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgRestake) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: derth/<validator>, nothing in or out.
func (m *MsgRestake) StakeLanes() StakeLanes { return StakeLanes{Denom: DerthDenom(m.Validator)} }

// SighashFields: StakeFields, Bytes(validator).
func (m *MsgRestake) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator))), nil
}

func (m *MsgRestake) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	return m.Stake.shape(true, false)
}

// ---- MsgUndelegate --------------------------------------------------------

func (m *MsgUndelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUndelegate) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUndelegate) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: derth/<validator>, amount leaving.
func (m *MsgUndelegate) StakeLanes() StakeLanes {
	return StakeLanes{Denom: DerthDenom(m.Validator), VOut: m.Amount}
}

// SighashFields: StakeFields, Bytes(validator), amount, pc,
// Bytes(ciphertext).
func (m *MsgUndelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Amount), pc,
		privacy.Bytes(m.Ciphertext)), nil
}

// ValidateBasic: the proof spends derth and creates the change (or a zero
// note); pc and ciphertext name the payout notes.
func (m *MsgUndelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := positive("amount", m.Amount); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if err := checkNoteOut(m.Pc, m.Ciphertext); err != nil {
		return err
	}
	return m.Stake.shape(true, false)
}

// ---- MsgRedelegate --------------------------------------------------------

func (m *MsgRedelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgRedelegate) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgRedelegate) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: derth/<src>, amount leaving; the credit lane derth/<dst>,
// crediting dst_derth labelled with move_time.
func (m *MsgRedelegate) StakeLanes() StakeLanes {
	return StakeLanes{Denom: DerthDenom(m.SrcValidator), VOut: m.Amount,
		CreditDenom: DerthDenom(m.DstValidator), CreditIn: m.DstDerth, CreditMoveTime: m.MoveTime}
}

// MoveKey is the move's key: its credit nullifier (the label's move_key).
func (m *MsgRedelegate) MoveKey() []byte { return m.Stake.CreditNullifier }

// SighashFields: StakeFields, Bytes(src_validator), Bytes(dst_validator),
// amount, dst_derth, move_time.
func (m *MsgRedelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.SrcValidator)), privacy.Bytes([]byte(m.DstValidator)),
		privacy.U64(m.Amount), privacy.U64(m.DstDerth), privacy.U64(m.MoveTime)), nil
}

// ValidateBasic: two different canonical validators; the proof spends
// derth/<src> (amount leaving, the change or a zero note back to the owner)
// and merges dst_derth into the owner's derth/<dst> note.
func (m *MsgRedelegate) ValidateBasic() error {
	if err := checkValidator(m.SrcValidator); err != nil {
		return err
	}
	if err := checkValidator(m.DstValidator); err != nil {
		return err
	}
	if m.SrcValidator == m.DstValidator {
		return errorsmod.Wrap(ErrRedelegation, "source and destination are the same validator")
	}
	if err := positive("amount", m.Amount); err != nil {
		return err
	}
	if err := positive("dst_derth", m.DstDerth); err != nil {
		return err
	}
	if err := positive("move_time", m.MoveTime); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	return m.Stake.shape(true, true)
}

// ---- MsgStakeVote ---------------------------------------------------------

// MaxVoteNotes is how many stake notes one vote proof carries (circuits/vote
// MAX_NOTES): every MsgStakeVote has exactly this many vote nullifier slots.
// One note per validator (ORCHARD_DESIGN.md section 20) needs one; the second
// covers a note made beside a labelled one.
const MaxVoteNotes = 2

// VoteProofInputs is the vote circuit's public input count: note_root,
// nf_root, debt_root, asset, weight, proposal_id, MaxVoteNotes vote
// nullifiers, sighash.
const VoteProofInputs = 7 + MaxVoteNotes

func (m *MsgStakeVote) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgStakeVote) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }

// SighashFields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)),
// weight, vote_nullifiers[0..MaxVoteNotes-1], debt_root.
func (m *MsgStakeVote) SighashFields(address.Codec) ([]fr.Element, error) {
	if len(m.VoteNullifiers) != MaxVoteNotes {
		return nil, errorsmod.Wrapf(ErrInvalidMsg, "a stake vote carries exactly %d vote nullifiers", MaxVoteNotes)
	}
	out := []fr.Element{privacy.U64(m.ProposalId), privacy.Bytes([]byte(m.Validator)),
		privacy.Bytes(OptionsBytes(m.Options)), privacy.U64(m.Weight)}
	for _, b := range m.VoteNullifiers {
		vnf, err := field("vote_nullifier", b)
		if err != nil {
			return nil, err
		}
		out = append(out, vnf)
	}
	root, err := field("debt_root", m.DebtRoot)
	if err != nil {
		return nil, err
	}
	return append(out, root), nil
}

// UsedVoteNullifiers is the msg's non-zero vote nullifiers, in order (one
// per note voted). Call after ValidateBasic.
func (m *MsgStakeVote) UsedVoteNullifiers() [][]byte {
	var out [][]byte
	for _, b := range m.VoteNullifiers {
		if !isZero(b) {
			out = append(out, b)
		}
	}
	return out
}

// ValidateBasic: exactly MaxVoteNotes vote nullifier slots, canonical field
// elements, the used ones first (at least one), distinct, the rest zero; a
// positive weight of at most three significant digits.
func (m *MsgStakeVote) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := positive("weight", m.Weight); err != nil {
		return err
	}
	if err := CheckVoteWeight(m.Weight); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if err := shieldedtypes.CheckProofLength(m.Proof); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "vote proof: %v", err)
	}
	if _, err := field("debt_root", m.DebtRoot); err != nil {
		return err
	}
	if len(m.VoteNullifiers) != MaxVoteNotes {
		return errorsmod.Wrapf(ErrInvalidMsg, "a stake vote carries exactly %d vote nullifiers (zero for an unused slot)", MaxVoteNotes)
	}
	seen := map[string]bool{}
	zeros := false
	for i, b := range m.VoteNullifiers {
		vnf, err := field("vote_nullifier", b)
		if err != nil {
			return err
		}
		if vnf.IsZero() {
			zeros = true
			continue
		}
		if zeros {
			return errorsmod.Wrapf(ErrInvalidMsg, "vote nullifier %d follows an unused slot: used slots come first", i)
		}
		if seen[string(b)] {
			return errorsmod.Wrap(ErrInvalidMsg, "repeated vote nullifier: a note votes once")
		}
		seen[string(b)] = true
	}
	if len(seen) == 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "a stake vote votes at least one note")
	}
	return ValidateOptions(m.Options)
}

// VotePublicInputs lays out the vote circuit's public inputs: note_root,
// nf_root (the proposal's snapshot roots), debt_root (the msg's, checked to
// be the current one), asset = AssetID(derth/<validator>), weight,
// proposal_id, vote_nullifiers[0..MaxVoteNotes-1], sighash. Call after
// ValidateBasic.
func (m *MsgStakeVote) VotePublicInputs(noteRoot, nfRoot []byte, sighash fr.Element) [][]byte {
	out := [][]byte{
		noteRoot, nfRoot, m.DebtRoot, privacy.FieldBytes(privacy.AssetID(DerthDenom(m.Validator))),
		privacy.FieldBytes(privacy.U64(m.Weight)), privacy.FieldBytes(privacy.U64(m.ProposalId)),
	}
	out = append(out, m.VoteNullifiers...)
	return append(out, privacy.FieldBytes(sighash))
}

// ---- positions ------------------------------------------------------------

func (m *MsgLockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgLockPosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgLockPosition) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: derth/<validator>, amount leaving into the position.
func (m *MsgLockPosition) StakeLanes() StakeLanes {
	return StakeLanes{Denom: DerthDenom(m.Validator), VOut: m.Amount}
}

// SighashFields: StakeFields, Bytes(validator), amount,
// Bytes(SplitsBytes(splits)).
func (m *MsgLockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Amount),
		privacy.Bytes(SplitsBytes(m.Splits))), nil
}

func (m *MsgLockPosition) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := positive("amount", m.Amount); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if len(m.Splits) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrap(ErrInvalidMsg, "too many splits")
	}
	return m.Stake.shape(true, false)
}

func (m *MsgUpdatePosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUpdatePosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUpdatePosition) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: none (the proof shows only the owner tag).
func (m *MsgUpdatePosition) StakeLanes() StakeLanes { return StakeLanes{} }

// SighashFields: StakeFields, position_id, Bytes(SplitsBytes(splits)).
func (m *MsgUpdatePosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId), privacy.Bytes(SplitsBytes(m.Splits))), nil
}

func (m *MsgUpdatePosition) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if len(m.Splits) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrap(ErrInvalidMsg, "too many splits")
	}
	return m.Stake.shape(false, false)
}

func (m *MsgUnlockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUnlockPosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUnlockPosition) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: none here. The keeper supplies derth/<the position's
// validator>, crediting the position's derth (UnlockLanes).
func (m *MsgUnlockPosition) StakeLanes() StakeLanes { return StakeLanes{} }

// UnlockLanes is what the chain supplies to an unlock of p: derth/<its
// validator>, crediting its derth.
func UnlockLanes(p Position) StakeLanes {
	return StakeLanes{Denom: DerthDenom(p.Validator), VIn: p.Derth.Uint64()}
}

// SighashFields: StakeFields, position_id.
func (m *MsgUnlockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId)), nil
}

// ValidateBasic: the proof merges the position's derth into the owner's
// note (or pads).
func (m *MsgUnlockPosition) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	return m.Stake.shape(true, false)
}

func (m *MsgPositionVote) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgPositionVote) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgPositionVote) StakeProofOf() *StakeProof               { return &m.Stake }

// StakeLanes: none (the proof shows only the owner tag).
func (m *MsgPositionVote) StakeLanes() StakeLanes { return StakeLanes{} }

// SighashFields: StakeFields, position_id, proposal_id,
// Bytes(OptionsBytes(options)).
func (m *MsgPositionVote) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId), privacy.U64(m.ProposalId),
		privacy.Bytes(OptionsBytes(m.Options))), nil
}

func (m *MsgPositionVote) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if err := ValidateOptions(m.Options); err != nil {
		return err
	}
	return m.Stake.shape(false, false)
}

// ---- MsgUpdateParams ------------------------------------------------------

func (m *MsgUpdateParams) ValidateBasic() error { return m.Params.Validate() }
