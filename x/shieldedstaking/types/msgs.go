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
const StakeProofInputs = 11

var zero32 = make([]byte, 32)

func isZero(b []byte) bool { return bytes.Equal(b, zero32) }

// StakeAsset is a stake denom's asset id (zk/privacy.AssetID): the stake
// circuit's public asset. 0 for a msg naming no stake denom (a position's
// update, unlock or vote, whose proof spends and creates nothing).
func StakeAsset(denom string) fr.Element {
	if denom == "" {
		return fr.Element{}
	}
	return privacy.AssetID(denom)
}

// ValidateBasic checks a stake proof's shape: a proof, canonical 32-byte
// fields, exactly two nullifiers and two commitments (zero for none), the
// spent ones distinct, exactly two ciphertext slots (a 153-byte wallet stake
// ciphertext for a non-zero commitment, empty for a zero one).
func (p *StakeProof) ValidateBasic() error {
	if err := shieldedtypes.CheckProofLength(p.Proof); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "stake proof: %v", err)
	}
	if _, err := field("stake anchor", p.Anchor); err != nil {
		return err
	}
	if _, err := field("stake spc_mint", p.SpcMint); err != nil {
		return err
	}
	if _, err := field("stake owner_tag", p.OwnerTag); err != nil {
		return err
	}
	if len(p.Nullifiers) != 2 || len(p.Commitments) != 2 {
		return errorsmod.Wrap(ErrInvalidMsg, "a stake proof carries exactly two nullifiers and two commitments")
	}
	for i := range 2 {
		if _, err := field("stake nullifier", p.Nullifiers[i]); err != nil {
			return err
		}
		if _, err := field("stake commitment", p.Commitments[i]); err != nil {
			return err
		}
	}
	if !isZero(p.Nullifiers[0]) && bytes.Equal(p.Nullifiers[0], p.Nullifiers[1]) {
		return errorsmod.Wrap(ErrInvalidMsg, "duplicate stake nullifier")
	}
	// One ciphertext slot per commitment, always both: empty for an unused
	// (zero) output, present for a created note. A missing slot and an empty
	// one sighash alike, so allowing both was two encodings of one msg.
	if len(p.Ciphertexts) != 2 {
		return errorsmod.Wrap(ErrInvalidMsg, "a stake proof carries exactly two ciphertexts (empty for a zero commitment)")
	}
	for i, ct := range p.Ciphertexts {
		if isZero(p.Commitments[i]) != (len(ct) == 0) {
			return errorsmod.Wrapf(ErrInvalidMsg, "ciphertext %d must be present iff commitment %d is non-zero", i, i)
		}
		if len(ct) != 0 && len(ct) != privacy.WalletStakeCiphertextBytes {
			return errorsmod.Wrapf(ErrInvalidMsg, "ciphertext %d must be exactly %d bytes (wallet stake note), got %d",
				i, privacy.WalletStakeCiphertextBytes, len(ct))
		}
	}
	return nil
}

// mints checks spc_ciphertext against whether the msg has the chain mint a
// stake note to spc_mint: then the note's blind stake ciphertext
// (zk/privacy.EncryptBlindStakeNote, 177 bytes) is required, else none.
func (p *StakeProof) mints(mints bool) error {
	if !mints {
		if len(p.SpcCiphertext) != 0 {
			return errorsmod.Wrap(ErrInvalidMsg, "spc_ciphertext is only for a msg that mints a stake note")
		}
		return nil
	}
	if err := shieldedtypes.CheckBlindCiphertext("spc_ciphertext", p.SpcCiphertext); err != nil {
		return errorsmod.Wrap(ErrInvalidMsg, err.Error())
	}
	return nil
}

// SpentNullifiers is the proof's non-zero nullifiers, in order.
func (p *StakeProof) SpentNullifiers() [][]byte {
	var out [][]byte
	for _, nf := range p.Nullifiers {
		if !isZero(nf) {
			out = append(out, nf)
		}
	}
	return out
}

// Outputs is the proof's non-zero commitments, in order, each with its
// ciphertext (nil when none).
func (p *StakeProof) Outputs() (cms, cts [][]byte) {
	for i, cm := range p.Commitments {
		if isZero(cm) {
			continue
		}
		var ct []byte
		if i < len(p.Ciphertexts) {
			ct = p.Ciphertexts[i]
		}
		cms, cts = append(cms, cm), append(cts, ct)
	}
	return cms, cts
}

// shape checks how many notes the proof spends (at least minSpends, none
// when minSpends is 0) and whether it may create any.
func (p *StakeProof) shape(minSpends int, creates bool) error {
	if err := p.ValidateBasic(); err != nil {
		return err
	}
	if n := len(p.SpentNullifiers()); n < minSpends {
		return errorsmod.Wrapf(ErrInvalidMsg, "the stake proof must spend at least %d note(s)", minSpends)
	} else if minSpends == 0 && n != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "the stake proof spends nothing for this msg")
	}
	if cms, _ := p.Outputs(); !creates && len(cms) != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "the stake proof creates no note for this msg")
	}
	return nil
}

func fieldOrZero(b []byte) fr.Element {
	e, _ := privacy.FieldFromBytes(b)
	return e
}

// StakeFields are the stake proof's values every staking msg's sighash binds
// first: anchor, nf_0, nf_1, cm_0, cm_1, Bytes(ct_0), Bytes(ct_1), spc_mint,
// owner_tag, Bytes(spc_ciphertext) (an absent ciphertext is Bytes of
// nothing).
func (p *StakeProof) StakeFields() []fr.Element {
	at := func(xs [][]byte, i int) []byte {
		if i < len(xs) {
			return xs[i]
		}
		return nil
	}
	return []fr.Element{
		fieldOrZero(p.Anchor), fieldOrZero(at(p.Nullifiers, 0)), fieldOrZero(at(p.Nullifiers, 1)),
		fieldOrZero(at(p.Commitments, 0)), fieldOrZero(at(p.Commitments, 1)),
		privacy.Bytes(at(p.Ciphertexts, 0)), privacy.Bytes(at(p.Ciphertexts, 1)),
		fieldOrZero(p.SpcMint), fieldOrZero(p.OwnerTag), privacy.Bytes(p.SpcCiphertext),
	}
}

// PublicInputs lays out the stake circuit's public inputs: anchor, asset,
// nf_0, nf_1, cm_out_0, cm_out_1, v_in (always 0), v_out, spc_mint,
// owner_tag, sighash. Call after ValidateBasic.
func (p *StakeProof) PublicInputs(asset fr.Element, vOut uint64, sighash fr.Element) [][]byte {
	return [][]byte{
		p.Anchor, privacy.FieldBytes(asset), p.Nullifiers[0], p.Nullifiers[1], p.Commitments[0], p.Commitments[1],
		privacy.FieldBytes(privacy.U64(0)), privacy.FieldBytes(privacy.U64(vOut)), p.SpcMint, p.OwnerTag,
		privacy.FieldBytes(sighash),
	}
}

// StakeMsg is a staking msg with a stake proof: the proof, the stake denom
// its notes are ("" for none) and what leaves them (v_out).
type StakeMsg interface {
	shieldedtypes.PrivateMsg
	StakeProofOf() *StakeProof
	StakeDenom() string
	VOut() uint64
}

var (
	_ StakeMsg = (*MsgDelegate)(nil)
	_ StakeMsg = (*MsgRestake)(nil)
	_ StakeMsg = (*MsgUndelegate)(nil)
	_ StakeMsg = (*MsgLockPosition)(nil)
	_ StakeMsg = (*MsgUpdatePosition)(nil)
	_ StakeMsg = (*MsgUnlockPosition)(nil)
	_ StakeMsg = (*MsgPositionVote)(nil)
)

func withStake(p *StakeProof, fields ...fr.Element) []fr.Element {
	return append(p.StakeFields(), fields...)
}

// VoteWeightSigFigs is how many significant decimal digits a stake vote's
// weight may have. Minted note amounts are public (delegate, undelegate and
// unlock events), so an exact weight links a vote to the mint that made its
// note, and the same note's votes across proposals. Every wallet rounds the
// weight down to this many digits, and the chain refuses any other, so all
// weights fall in the same buckets (audit 6 C-L3). The circuit asks only
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
func (m *MsgDelegate) StakeDenom() string                      { return DerthDenom(m.Validator) }
func (m *MsgDelegate) VOut() uint64                            { return 0 }

// Delegated is the uerth delegated: what the bundle releases beyond the fee
// (amount, once ValidateBasic passed).
func (m *MsgDelegate) Delegated() uint64 { return released(m, BondDenom) }

// SighashFields: StakeFields, Bytes(validator), amount.
func (m *MsgDelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Amount)), nil
}

func (m *MsgDelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := positive("amount", m.Amount); err != nil {
		return err
	}
	if err := checkMoves(m, BondDenom, 0); err != nil {
		return err
	}
	if released(m, BondDenom) != m.Amount {
		return errorsmod.Wrap(ErrInvalidMsg, "the bundle must release amount uerth beyond its fee")
	}
	if err := m.Stake.shape(0, false); err != nil {
		return err
	}
	return m.Stake.mints(true)
}

// ---- MsgRestake -----------------------------------------------------------

func (m *MsgRestake) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgRestake) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgRestake) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgRestake) StakeDenom() string                      { return DerthDenom(m.Validator) }
func (m *MsgRestake) VOut() uint64                            { return 0 }

// SighashFields: StakeFields, Bytes(validator).
func (m *MsgRestake) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator))), nil
}

func (m *MsgRestake) ValidateBasic() error {
	if err := m.Stake.mints(false); err != nil {
		return err
	}
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if err := m.Stake.shape(1, true); err != nil {
		return err
	}
	if cms, _ := m.Stake.Outputs(); len(cms) == 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "a restake creates at least one note")
	}
	return nil
}

// ---- MsgUndelegate --------------------------------------------------------

func (m *MsgUndelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUndelegate) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUndelegate) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgUndelegate) StakeDenom() string                      { return DerthDenom(m.Validator) }
func (m *MsgUndelegate) VOut() uint64                            { return m.Amount }

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

// ValidateBasic: the proof spends derth and may return change; no stake note
// is minted (spc_ciphertext empty); pc and ciphertext name the payout notes.
func (m *MsgUndelegate) ValidateBasic() error {
	if err := m.Stake.mints(false); err != nil {
		return err
	}
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
	return m.Stake.shape(1, true)
}

// ---- MsgStakeVote ---------------------------------------------------------

// MaxVoteNotes is how many stake notes one vote proof carries (circuits/vote
// MAX_NOTES): every MsgStakeVote has exactly this many vote nullifier slots.
const MaxVoteNotes = 4

// VoteProofInputs is the vote circuit's public input count: note_root,
// nf_root, asset, weight, proposal_id, MaxVoteNotes vote nullifiers, sighash.
const VoteProofInputs = 6 + MaxVoteNotes

func (m *MsgStakeVote) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgStakeVote) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }

// SighashFields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)),
// weight, vote_nullifiers[0..MaxVoteNotes-1].
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
	return out, nil
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
// nf_root (the proposal's snapshot roots), asset = AssetID(derth/<validator>),
// weight, proposal_id, vote_nullifiers[0..MaxVoteNotes-1], sighash. Call after
// ValidateBasic.
func (m *MsgStakeVote) VotePublicInputs(noteRoot, nfRoot []byte, sighash fr.Element) [][]byte {
	out := [][]byte{
		noteRoot, nfRoot, privacy.FieldBytes(privacy.AssetID(DerthDenom(m.Validator))),
		privacy.FieldBytes(privacy.U64(m.Weight)), privacy.FieldBytes(privacy.U64(m.ProposalId)),
	}
	out = append(out, m.VoteNullifiers...)
	return append(out, privacy.FieldBytes(sighash))
}

// ---- positions ------------------------------------------------------------

func (m *MsgLockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgLockPosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgLockPosition) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgLockPosition) StakeDenom() string                      { return DerthDenom(m.Validator) }
func (m *MsgLockPosition) VOut() uint64                            { return m.Amount }

// SighashFields: StakeFields, Bytes(validator), amount,
// Bytes(SplitsBytes(splits)).
func (m *MsgLockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Amount),
		privacy.Bytes(SplitsBytes(m.Splits))), nil
}

func (m *MsgLockPosition) ValidateBasic() error {
	if err := m.Stake.mints(false); err != nil {
		return err
	}
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
	return m.Stake.shape(1, true)
}

func (m *MsgUpdatePosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUpdatePosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUpdatePosition) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgUpdatePosition) StakeDenom() string                      { return "" }
func (m *MsgUpdatePosition) VOut() uint64                            { return 0 }

// SighashFields: StakeFields, position_id, Bytes(SplitsBytes(splits)).
func (m *MsgUpdatePosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId), privacy.Bytes(SplitsBytes(m.Splits))), nil
}

func (m *MsgUpdatePosition) ValidateBasic() error {
	if err := m.Stake.mints(false); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if len(m.Splits) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrap(ErrInvalidMsg, "too many splits")
	}
	return m.Stake.shape(0, false)
}

func (m *MsgUnlockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUnlockPosition) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgUnlockPosition) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgUnlockPosition) StakeDenom() string                      { return "" }
func (m *MsgUnlockPosition) VOut() uint64                            { return 0 }

// SighashFields: StakeFields, position_id.
func (m *MsgUnlockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId)), nil
}

func (m *MsgUnlockPosition) ValidateBasic() error {
	if err := m.Stake.mints(true); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	return m.Stake.shape(0, false)
}

func (m *MsgPositionVote) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgPositionVote) PrivateFee() uint64                      { return shieldedtypes.FeeAfter(m, 0) }
func (m *MsgPositionVote) StakeProofOf() *StakeProof               { return &m.Stake }
func (m *MsgPositionVote) StakeDenom() string                      { return "" }
func (m *MsgPositionVote) VOut() uint64                            { return 0 }

// SighashFields: StakeFields, position_id, proposal_id,
// Bytes(OptionsBytes(options)).
func (m *MsgPositionVote) SighashFields(address.Codec) ([]fr.Element, error) {
	return withStake(&m.Stake, privacy.U64(m.PositionId), privacy.U64(m.ProposalId),
		privacy.Bytes(OptionsBytes(m.Options))), nil
}

func (m *MsgPositionVote) ValidateBasic() error {
	if err := m.Stake.mints(false); err != nil {
		return err
	}
	if err := checkMoves(m, "", 0); err != nil {
		return err
	}
	if err := ValidateOptions(m.Options); err != nil {
		return err
	}
	return m.Stake.shape(0, false)
}

// ---- MsgUpdateParams ------------------------------------------------------

func (m *MsgUpdateParams) ValidateBasic() error { return m.Params.Validate() }
