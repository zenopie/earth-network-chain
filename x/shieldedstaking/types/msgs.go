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

	_ shieldedtypes.FeeFromOutputMsg = (*MsgClaimUnbonding)(nil)
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

// checkMoves checks msg's release map: a positive fee (unless outputFee pays
// it), and exactly denom released beyond it (a positive amount), or nothing
// when denom is "".
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

func bundle(b *shieldedtypes.Bundle) []*shieldedtypes.Bundle { return []*shieldedtypes.Bundle{b} }

// ---- MsgDelegate ----------------------------------------------------------

func (m *MsgDelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgDelegate) PrivateFee() uint64                      { return m.Fee }

// Amount is the uerth delegated: the bundle's uerth balance less the fee.
func (m *MsgDelegate) Amount() uint64 { return released(m, BondDenom) }

// SighashFields: Bytes(validator), pc, Bytes(ciphertext), fee.
func (m *MsgDelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes([]byte(m.Validator)), pc, privacy.Bytes(m.Ciphertext), privacy.U64(m.Fee)}, nil
}

func (m *MsgDelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, BondDenom, 0); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgUndelegate --------------------------------------------------------

func (m *MsgUndelegate) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUndelegate) PrivateFee() uint64                      { return m.Fee }

// Amount is the derth undelegated.
func (m *MsgUndelegate) Amount() uint64 { return released(m, DerthDenom(m.Validator)) }

// SighashFields: Bytes(validator), pc, Bytes(ciphertext), fee.
func (m *MsgUndelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes([]byte(m.Validator)), pc, privacy.Bytes(m.Ciphertext), privacy.U64(m.Fee)}, nil
}

func (m *MsgUndelegate) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, DerthDenom(m.Validator), 0); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgClaimUnbonding ----------------------------------------------------

func (m *MsgClaimUnbonding) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgClaimUnbonding) PrivateFee() uint64                      { return m.Fee }

// Amount is the unbond denom claimed.
func (m *MsgClaimUnbonding) Amount() uint64 { return released(m, UnbondDenom(m.Validator, m.Epoch)) }

// SighashFields: Bytes(validator), epoch, pc, Bytes(ciphertext),
// fee_from_output, fee.
func (m *MsgClaimUnbonding) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes([]byte(m.Validator)), privacy.U64(m.Epoch), pc, privacy.Bytes(m.Ciphertext),
		privacy.U64(m.FeeFromOutput), privacy.U64(m.Fee)}, nil
}

// OutputFee implements x/shielded's FeeFromOutputMsg.
func (m *MsgClaimUnbonding) OutputFee() uint64 { return m.FeeFromOutput }

func (m *MsgClaimUnbonding) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, UnbondDenom(m.Validator, m.Epoch), m.FeeFromOutput); err != nil {
		return err
	}
	return checkNoteOut(m.Pc, m.Ciphertext)
}

// ---- MsgStakeVote ---------------------------------------------------------

// PrivateBundles is the vote bundle, then the fee bundle.
func (m *MsgStakeVote) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Bundle, &m.FeeBundle}
}

func (m *MsgStakeVote) PrivateFee() uint64 { return m.Fee }

// Weight is the derth the vote bundle releases: the vote's weight.
func (m *MsgStakeVote) Weight() uint64 { return m.Bundle.Balance(DerthDenom(m.Validator)) }

// SighashFields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)),
// pc, Bytes(ciphertext), fee. Both bundles' digests precede them, so neither
// bundle can be paired with another.
func (m *MsgStakeVote) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.U64(m.ProposalId), privacy.Bytes([]byte(m.Validator)),
		privacy.Bytes(OptionsBytes(m.Options)), pc, privacy.Bytes(m.Ciphertext), privacy.U64(m.Fee)}, nil
}

func onlyBalance(b *shieldedtypes.Bundle, denom string) bool {
	return len(b.Balances) == 1 && b.Balances[0].Denom == denom && b.Balances[0].Amount > 0
}

func (m *MsgStakeVote) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, DerthDenom(m.Validator), 0); err != nil {
		return err
	}
	// The vote bundle, proven against the snapshot root, releases only the
	// weight; the fee bundle, against current roots, pays exactly the fee.
	if !onlyBalance(&m.Bundle, DerthDenom(m.Validator)) {
		return errorsmod.Wrapf(ErrInvalidMsg, "the vote bundle's only balance is %s, the weight", DerthDenom(m.Validator))
	}
	if !onlyBalance(&m.FeeBundle, shieldedtypes.FeeDenom) || m.FeeBundle.Balances[0].Amount != m.Fee {
		return errorsmod.Wrap(ErrInvalidMsg, "fee_bundle's only balance is the uerth fee")
	}
	if err := checkNoteOut(m.Pc, m.Ciphertext); err != nil {
		return err
	}
	return ValidateOptions(m.Options)
}

// ---- positions ------------------------------------------------------------

func (m *MsgLockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgLockPosition) PrivateFee() uint64                      { return m.Fee }

// Amount is the derth locked.
func (m *MsgLockPosition) Amount() uint64 { return released(m, DerthDenom(m.Validator)) }

// SighashFields: Bytes(validator), Bytes(pubkey), Bytes(SplitsBytes(splits)),
// fee.
func (m *MsgLockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.Bytes([]byte(m.Validator)), privacy.Bytes(m.Pubkey),
		privacy.Bytes(SplitsBytes(m.Splits)), privacy.U64(m.Fee)}, nil
}

func (m *MsgLockPosition) ValidateBasic() error {
	if err := checkValidator(m.Validator); err != nil {
		return err
	}
	if err := checkMoves(m, DerthDenom(m.Validator), 0); err != nil {
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

func (m *MsgUpdatePosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUpdatePosition) PrivateFee() uint64                      { return m.Fee }

// SighashFields: position_id, Bytes(SplitsBytes(splits)), Bytes(signature),
// fee.
func (m *MsgUpdatePosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.U64(m.PositionId), privacy.Bytes(SplitsBytes(m.Splits)),
		privacy.Bytes(m.Signature), privacy.U64(m.Fee)}, nil
}

func (m *MsgUpdatePosition) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
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

func (m *MsgUnlockPosition) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgUnlockPosition) PrivateFee() uint64                      { return m.Fee }

// SighashFields: position_id, pc, Bytes(ciphertext), Bytes(signature), fee.
func (m *MsgUnlockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.U64(m.PositionId), pc, privacy.Bytes(m.Ciphertext),
		privacy.Bytes(m.Signature), privacy.U64(m.Fee)}, nil
}

func (m *MsgUnlockPosition) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
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

func (m *MsgPositionVote) PrivateBundles() []*shieldedtypes.Bundle { return bundle(&m.Bundle) }
func (m *MsgPositionVote) PrivateFee() uint64                      { return m.Fee }

// SighashFields: position_id, proposal_id, Bytes(OptionsBytes(options)),
// Bytes(signature), fee.
func (m *MsgPositionVote) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.U64(m.PositionId), privacy.U64(m.ProposalId),
		privacy.Bytes(OptionsBytes(m.Options)), privacy.Bytes(m.Signature), privacy.U64(m.Fee)}, nil
}

func (m *MsgPositionVote) ValidateBasic() error {
	if err := checkMoves(m, "", 0); err != nil {
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
