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

// Msg type URLs: the kind every signal binds first.
const (
	TypeMsgDelegate       = "/earth.shieldedstaking.v1.MsgDelegate"
	TypeMsgUndelegate     = "/earth.shieldedstaking.v1.MsgUndelegate"
	TypeMsgClaimUnbonding = "/earth.shieldedstaking.v1.MsgClaimUnbonding"
	TypeMsgStakeVote      = "/earth.shieldedstaking.v1.MsgStakeVote"
	TypeMsgLockPosition   = "/earth.shieldedstaking.v1.MsgLockPosition"
	TypeMsgUpdatePosition = "/earth.shieldedstaking.v1.MsgUpdatePosition"
	TypeMsgUnlockPosition = "/earth.shieldedstaking.v1.MsgUnlockPosition"
	TypeMsgPositionVote   = "/earth.shieldedstaking.v1.MsgPositionVote"
)

var (
	_ shieldedtypes.PrivateMsg = (*MsgDelegate)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUndelegate)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgClaimUnbonding)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgStakeVote)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgLockPosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUpdatePosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgUnlockPosition)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgPositionVote)(nil)

	_ shieldedtypes.MultiTransferMsg = (*MsgStakeVote)(nil)
	_ shieldedtypes.FeeFromOutputMsg = (*MsgClaimUnbonding)(nil)
)

// ---- field encodings the signals and position signatures bind ------------
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

func checkNoteOut(pc, ct []byte) error {
	if _, err := field("pc", pc); err != nil {
		return err
	}
	if len(ct) > shieldedtypes.MaxCiphertextBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "ciphertext exceeds %d bytes", shieldedtypes.MaxCiphertextBytes)
	}
	return nil
}

func checkMoves(t *shieldedtypes.Transfer, denom string) error {
	if err := t.ValidateBasic(); err != nil {
		return err
	}
	if denom == "" {
		if t.ValueOut != 0 {
			return errorsmod.Wrap(ErrInvalidMsg, "this msg moves no value: value_out must be 0")
		}
		return nil
	}
	if t.ValueOut == 0 || t.DenomOut != denom {
		return errorsmod.Wrapf(ErrInvalidMsg, "transfer must release a positive amount of %s", denom)
	}
	return nil
}

func checkValidator(v string) error {
	if v == "" || len(v) > 128 || bytes.ContainsAny([]byte(v), "/ ") {
		return errorsmod.Wrap(ErrInvalidMsg, "invalid validator")
	}
	return nil
}

func checkSig(sig []byte) error {
	if len(sig) != 64 {
		return errorsmod.Wrap(ErrSignature, "signature must be 64 bytes (r || s)")
	}
	return nil
}

func spend(msgType, chainID string, t *shieldedtypes.Transfer, extra ...fr.Element) fr.Element {
	return privacy.SpendSignal(msgType, chainID, t.Ciphertexts3(), extra...)
}

// ---- MsgDelegate ----------------------------------------------------------

func (m *MsgDelegate) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgDelegate) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return spend(TypeMsgDelegate, chainID, &m.Transfer, privacy.Bytes([]byte(m.Validator)), pc, privacy.Bytes(m.Ciphertext)), nil
}

func (m *MsgDelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(&m.Transfer, BondDenom); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgUndelegate --------------------------------------------------------

func (m *MsgUndelegate) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgUndelegate) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return spend(TypeMsgUndelegate, chainID, &m.Transfer, privacy.Bytes([]byte(m.Validator)), pc, privacy.Bytes(m.Ciphertext)), nil
}

func (m *MsgUndelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(&m.Transfer, DerthDenom(m.Validator)); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgClaimUnbonding ----------------------------------------------------

func (m *MsgClaimUnbonding) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgClaimUnbonding) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return spend(TypeMsgClaimUnbonding, chainID, &m.Transfer, privacy.Bytes([]byte(m.Validator)),
		privacy.U64(m.Epoch), pc, privacy.Bytes(m.Ciphertext), privacy.U64(m.FeeFromOutput)), nil
}

// OutputFee implements x/shielded's FeeFromOutputMsg.
func (m *MsgClaimUnbonding) OutputFee() uint64 { return m.FeeFromOutput }

func (m *MsgClaimUnbonding) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(&m.Transfer, UnbondDenom(m.Validator, m.Epoch)); err != nil {
		return err
	}
	if err := shieldedtypes.ValidateTransfers(m); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgStakeVote ---------------------------------------------------------

func (m *MsgStakeVote) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

// PrivateTransfers is the vote transfer, then the fee transfer.
func (m *MsgStakeVote) PrivateTransfers() []*shieldedtypes.Transfer {
	return []*shieldedtypes.Transfer{&m.Transfer, &m.FeeTransfer}
}

// Signal binds both transfers (each one's ciphertexts and nullifiers), the
// proposal, the vote and where the derth is minted back. Both proofs carry
// it, so neither transfer can be paired with another.
func (m *MsgStakeVote) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return shieldedtypes.MultiSignal(TypeMsgStakeVote, chainID, m.PrivateTransfers(), privacy.U64(m.ProposalId),
		privacy.Bytes([]byte(m.Validator)), privacy.Bytes(OptionsBytes(m.Options)), pc, privacy.Bytes(m.Ciphertext))
}

func (m *MsgStakeVote) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(&m.Transfer, DerthDenom(m.Validator)); err != nil {
		return err
	}
	// The vote transfer is proven against the snapshot root and pays no fee
	// (its slot 2 is a dummy); the fee transfer pays it, against a current
	// root, and moves nothing else.
	if m.Transfer.Fee != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "the vote transfer pays no fee: fee_transfer does")
	}
	if err := checkMoves(&m.FeeTransfer, ""); err != nil {
		return errorsmod.Wrap(err, "fee_transfer")
	}
	if m.FeeTransfer.Fee == 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "fee_transfer must pay a fee")
	}
	if err := shieldedtypes.ValidateTransfers(m); err != nil {
		return err
	}
	if err := checkNoteOut(m.Pc, m.Ciphertext); err != nil {
		return err
	}
	return ValidateOptions(m.Options)
}

// ---- positions ------------------------------------------------------------

func (m *MsgLockPosition) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgLockPosition) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return spend(TypeMsgLockPosition, chainID, &m.Transfer, privacy.Bytes([]byte(m.Validator)),
		privacy.Bytes(m.Pubkey), privacy.Bytes(SplitsBytes(m.Splits))), nil
}

func (m *MsgLockPosition) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(&m.Transfer, DerthDenom(m.Validator)); err != nil {
		return err
	}
	if len(m.Pubkey) != 33 || (m.Pubkey[0] != 2 && m.Pubkey[0] != 3) {
		return errorsmod.Wrap(ErrInvalidMsg, "pubkey must be a 33-byte compressed secp256k1 key")
	}
	if len(m.Splits) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrap(ErrInvalidMsg, "too many splits")
	}
	return nil
}

func (m *MsgUpdatePosition) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgUpdatePosition) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return spend(TypeMsgUpdatePosition, chainID, &m.Transfer, privacy.U64(m.PositionId),
		privacy.Bytes(SplitsBytes(m.Splits)), privacy.Bytes(m.Signature)), nil
}

func (m *MsgUpdatePosition) ValidateBasic() error {
	if err := checkMoves(&m.Transfer, ""); err != nil {
		return err
	}
	if len(m.Splits) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrap(ErrInvalidMsg, "too many splits")
	}
	return checkSig(m.Signature)
}

// SignPayload is what the position key signs (with the nonce and chain id,
// see PositionSignBytes).
func (m *MsgUpdatePosition) SignPayload() []byte { return SplitsBytes(m.Splits) }

func (m *MsgUnlockPosition) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgUnlockPosition) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return spend(TypeMsgUnlockPosition, chainID, &m.Transfer, privacy.U64(m.PositionId), pc,
		privacy.Bytes(m.Ciphertext), privacy.Bytes(m.Signature)), nil
}

func (m *MsgUnlockPosition) ValidateBasic() error {
	if err := checkMoves(&m.Transfer, ""); err != nil {
		return err
	}
	if err := checkNoteOut(m.Pc, m.Ciphertext); err != nil {
		return err
	}
	return checkSig(m.Signature)
}

// SignPayload is pc || ciphertext: whoever relays the unlock cannot redirect
// the note.
func (m *MsgUnlockPosition) SignPayload() []byte {
	return append(append([]byte{}, m.Pc...), m.Ciphertext...)
}

func (m *MsgPositionVote) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

func (m *MsgPositionVote) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return spend(TypeMsgPositionVote, chainID, &m.Transfer, privacy.U64(m.PositionId), privacy.U64(m.ProposalId),
		privacy.Bytes(OptionsBytes(m.Options)), privacy.Bytes(m.Signature)), nil
}

func (m *MsgPositionVote) ValidateBasic() error {
	if err := checkMoves(&m.Transfer, ""); err != nil {
		return err
	}
	if err := ValidateOptions(m.Options); err != nil {
		return err
	}
	return checkSig(m.Signature)
}

// SignPayload is proposal_id (big-endian u64) || OptionsBytes.
func (m *MsgPositionVote) SignPayload() []byte {
	return append(binary.BigEndian.AppendUint64(nil, m.ProposalId), OptionsBytes(m.Options)...)
}

// PositionSignBytes is the message a position key signs (secp256k1 over its
// sha256, low-S, 64-byte r||s):
//
//	"earth.shieldedstaking.position" 0x00 action 0x00 chain_id 0x00
//	position_id (u64 BE) nonce (u64 BE) payload
//
// action is "update", "unlock" or "vote"; nonce is the position's current
// nonce, bumped by every accepted signature.
func PositionSignBytes(chainID, action string, positionID, nonce uint64, payload []byte) []byte {
	b := []byte("earth.shieldedstaking.position")
	b = append(b, 0)
	b = append(b, action...)
	b = append(b, 0)
	b = append(b, chainID...)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint64(b, positionID)
	b = binary.BigEndian.AppendUint64(b, nonce)
	return append(b, payload...)
}

// ---- MsgUpdateParams ------------------------------------------------------

func (m *MsgUpdateParams) ValidateBasic() error { return m.Params.Validate() }
