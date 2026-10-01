package types

import (
	"math/big"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

var (
	_ shieldedtypes.PrivateMsg = (*MsgRegister)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgClaimAnml)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgSetCaretaker)(nil)

	_ sdk.HasValidateBasic = (*MsgRegister)(nil)
	_ sdk.HasValidateBasic = (*MsgClaimAnml)(nil)
	_ sdk.HasValidateBasic = (*MsgSetCaretaker)(nil)
)

// MaxPublicSignals bounds a passport proof's public input count. The lean_poa
// circuits have four.
const MaxPublicSignals = 16

// MaxDscDerBytes bounds the Document Signer certificate a registration carries.
const MaxDscDerBytes = 8 * 1024

// MaxNoteCiphertextBytes bounds a note ciphertext, as the shielded pool does.
const MaxNoteCiphertextBytes = shieldedtypes.MaxCiphertextBytes

// Field parses a 32-byte canonical field element, wrapping the error.
func Field(what string, b []byte) (fr.Element, error) {
	e, err := privacy.FieldFromBytes(b)
	if err != nil {
		return e, errorsmod.Wrapf(ErrInvalidMsg, "%s: %v", what, err)
	}
	return e, nil
}

// OptionalField is Field for a value that may be absent (empty = 0).
func OptionalField(what string, b []byte) (fr.Element, error) {
	if len(b) == 0 {
		return fr.Element{}, nil
	}
	return Field(what, b)
}

func checkCiphertext(what string, b []byte) error {
	if len(b) > MaxNoteCiphertextBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "%s exceeds %d bytes", what, MaxNoteCiphertextBytes)
	}
	return nil
}

// ValidateBasic checks a membership proof's shape.
func (m Membership) ValidateBasic() error {
	if len(m.Proof) == 0 || len(m.Proof) > shieldedtypes.MaxProofBytes {
		return errorsmod.Wrapf(ErrInvalidMembership, "proof must be 1..%d bytes", shieldedtypes.MaxProofBytes)
	}
	if _, err := Field("membership root", m.Root); err != nil {
		return err
	}
	if _, err := Field("membership nullifier", m.Nullifier); err != nil {
		return err
	}
	return nil
}

// MembershipPublicInputs lays out the membership circuit's public inputs:
// root, scope, nullifier, signal, excluded_dsc, excluded_country,
// max_activation.
func MembershipPublicInputs(m Membership, scope, signal, excludedDsc, excludedCountry fr.Element, maxActivation uint64) [][]byte {
	return [][]byte{
		m.Root,
		privacy.FieldBytes(scope),
		m.Nullifier,
		privacy.FieldBytes(signal),
		privacy.FieldBytes(excludedDsc),
		privacy.FieldBytes(excludedCountry),
		privacy.FieldBytes(privacy.U64(maxActivation)),
	}
}

// ParseSignal parses one decimal passport public signal as a canonical field
// element. Anything at or above the modulus is refused: the verifier reduces
// mod p, so n and n+p would verify the same proof while differing byte for
// byte as dedup keys.
func ParseSignal(s string) (fr.Element, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.Cmp(fr.Modulus()) >= 0 {
		return fr.Element{}, ErrBadPublicInputs.Wrapf("bad public signal %q", s)
	}
	var e fr.Element
	e.SetBigInt(n)
	return e, nil
}

// --- MsgRegister ---------------------------------------------------------

// PrivateTransfer implements PrivateMsg.
func (m *MsgRegister) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Binding is the value the passport proof's address input must carry:
// zk/privacy.RegistrationBinding(idc, pc_anml, pc_erth, affiliate_pc).
func (m *MsgRegister) Binding() (fr.Element, error) {
	idc, err := Field("idc", m.Idc)
	if err != nil {
		return fr.Element{}, err
	}
	pcAnml, err := Field("pc_anml", m.PcAnml)
	if err != nil {
		return fr.Element{}, err
	}
	pcErth, err := Field("pc_erth", m.PcErth)
	if err != nil {
		return fr.Element{}, err
	}
	aff, err := OptionalField("affiliate_pc", m.AffiliatePc)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.RegistrationBinding(idc, pcAnml, pcErth, aff), nil
}

// Signal implements PrivateMsg. Fields: idc, pc_anml, Bytes(ciphertext_anml),
// pc_erth, Bytes(ciphertext_erth), affiliate_pc (0 for none),
// Bytes(affiliate_ciphertext), Bytes(signature_algorithm), then every public
// signal in order.
func (m *MsgRegister) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	idc, err := Field("idc", m.Idc)
	if err != nil {
		return fr.Element{}, err
	}
	pcAnml, err := Field("pc_anml", m.PcAnml)
	if err != nil {
		return fr.Element{}, err
	}
	pcErth, err := Field("pc_erth", m.PcErth)
	if err != nil {
		return fr.Element{}, err
	}
	aff, err := OptionalField("affiliate_pc", m.AffiliatePc)
	if err != nil {
		return fr.Element{}, err
	}
	extra := []fr.Element{
		idc, pcAnml, privacy.Bytes(m.CiphertextAnml), pcErth, privacy.Bytes(m.CiphertextErth),
		aff, privacy.Bytes(m.AffiliateCiphertext), privacy.Bytes([]byte(m.SignatureAlgorithm)),
	}
	for _, s := range m.PublicSignals {
		e, err := ParseSignal(s)
		if err != nil {
			return fr.Element{}, err
		}
		extra = append(extra, e)
	}
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, extra...)
}

// ValidateBasic checks everything that needs no state.
func (m *MsgRegister) ValidateBasic() error {
	if err := m.Fee.ValidateBasic(); err != nil {
		return err
	}
	if m.Fee.ValueOut != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "a registration's fee transfer releases nothing")
	}
	if len(m.Proof) == 0 || len(m.Proof) > shieldedtypes.MaxProofBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "proof must be 1..%d bytes", shieldedtypes.MaxProofBytes)
	}
	if len(m.PublicSignals) == 0 || len(m.PublicSignals) > MaxPublicSignals {
		return errorsmod.Wrapf(ErrInvalidMsg, "need 1..%d public signals", MaxPublicSignals)
	}
	for _, s := range m.PublicSignals {
		if _, err := ParseSignal(s); err != nil {
			return err
		}
	}
	if len(m.DscDer) > MaxDscDerBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "dsc_der exceeds %d bytes", MaxDscDerBytes)
	}
	if _, err := m.Binding(); err != nil {
		return err
	}
	if len(m.AffiliatePc) == 0 && len(m.AffiliateCiphertext) != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "affiliate_ciphertext without affiliate_pc")
	}
	for what, ct := range map[string][]byte{
		"ciphertext_anml": m.CiphertextAnml, "ciphertext_erth": m.CiphertextErth, "affiliate_ciphertext": m.AffiliateCiphertext,
	} {
		if err := checkCiphertext(what, ct); err != nil {
			return err
		}
	}
	return nil
}

// --- MsgClaimAnml --------------------------------------------------------

// PrivateTransfer implements PrivateMsg.
func (m *MsgClaimAnml) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Signal implements PrivateMsg. Fields: day, pc, Bytes(ciphertext).
func (m *MsgClaimAnml) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := Field("pc", m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, privacy.U64(m.Day), pc, privacy.Bytes(m.Ciphertext))
}

// ValidateBasic checks everything that needs no state.
func (m *MsgClaimAnml) ValidateBasic() error {
	if err := m.Fee.ValidateBasic(); err != nil {
		return err
	}
	if m.Fee.ValueOut != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "a claim's fee transfer releases nothing")
	}
	if err := m.Membership.ValidateBasic(); err != nil {
		return err
	}
	if _, err := Field("pc", m.Pc); err != nil {
		return err
	}
	return checkCiphertext("ciphertext", m.Ciphertext)
}

// --- MsgSetCaretaker -----------------------------------------------------

// PrivateTransfer implements PrivateMsg.
func (m *MsgSetCaretaker) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Signal implements PrivateMsg. Fields: option_id then percent, per entry.
func (m *MsgSetCaretaker) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	extra := make([]fr.Element, 0, 2*len(m.Percentages))
	for _, w := range m.Percentages {
		extra = append(extra, privacy.U64(w.OptionId), privacy.U64(w.Percent))
	}
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, extra...)
}

// ValidateBasic checks everything that needs no state. The split itself is
// checked by x/allocation, against the options as they stand.
func (m *MsgSetCaretaker) ValidateBasic() error {
	if err := m.Fee.ValidateBasic(); err != nil {
		return err
	}
	if m.Fee.ValueOut != 0 {
		return errorsmod.Wrap(ErrInvalidMsg, "a caretaker split's fee transfer releases nothing")
	}
	if len(m.Percentages) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrapf(ErrInvalidMsg, "split across %d options exceeds %d", len(m.Percentages), allocationtypes.MaxVoterOptions)
	}
	return m.Membership.ValidateBasic()
}

// MembershipStatement is what a membership proof must prove, beyond holding a
// leaf of the identity tree at its root: the chain fixes all of it from the
// msg and its own state, never from the prover.
type MembershipStatement struct {
	Scope       fr.Element
	Signal      fr.Element
	ExcludedDsc fr.Element
	// ExcludedCountry is CountryField of a country whose registrations may
	// not prove, or 0 for none.
	ExcludedCountry fr.Element
	// MaxActivation is the latest activated_at a leaf may carry, in unix
	// seconds. Negative clamps to 0: nobody qualifies.
	MaxActivation int64
}
