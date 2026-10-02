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
	_ shieldedtypes.PrivateMsg = (*MsgBindReferrer)(nil)

	_ sdk.HasValidateBasic = (*MsgRegister)(nil)
	_ sdk.HasValidateBasic = (*MsgClaimAnml)(nil)
	_ sdk.HasValidateBasic = (*MsgSetCaretaker)(nil)
	_ sdk.HasValidateBasic = (*MsgBindReferrer)(nil)
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

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgRegister) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgRegister) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// MaxAddressBytes bounds a bech32 address string in a msg.
const MaxAddressBytes = 128

// AffiliateField is the affiliate's place in the registration binding and
// signal: Bytes(its address bytes), or 0 for none.
func AffiliateField(ac address.Codec, affiliate string) (fr.Element, error) {
	if affiliate == "" {
		return fr.Element{}, nil
	}
	bz, err := ac.StringToBytes(affiliate)
	if err != nil {
		return fr.Element{}, errorsmod.Wrapf(ErrInvalidMsg, "affiliate: %v", err)
	}
	return privacy.Bytes(bz), nil
}

// Binding is the value the passport proof's address input must carry:
// zk/privacy.RegistrationBinding(idc, pc_anml, pc_erth, AffiliateField).
func (m *MsgRegister) Binding(ac address.Codec) (fr.Element, error) {
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
	aff, err := AffiliateField(ac, m.Affiliate)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.RegistrationBinding(idc, pcAnml, pcErth, aff), nil
}

// SighashFields implements PrivateMsg: idc, pc_anml, Bytes(ciphertext_anml),
// pc_erth, Bytes(ciphertext_erth), AffiliateField, Bytes(signature_algorithm),
// then every public signal in order.
func (m *MsgRegister) SighashFields(ac address.Codec) ([]fr.Element, error) {
	idc, err := Field("idc", m.Idc)
	if err != nil {
		return nil, err
	}
	pcAnml, err := Field("pc_anml", m.PcAnml)
	if err != nil {
		return nil, err
	}
	pcErth, err := Field("pc_erth", m.PcErth)
	if err != nil {
		return nil, err
	}
	aff, err := AffiliateField(ac, m.Affiliate)
	if err != nil {
		return nil, err
	}
	extra := []fr.Element{
		idc, pcAnml, privacy.Bytes(m.CiphertextAnml), pcErth, privacy.Bytes(m.CiphertextErth),
		aff, privacy.Bytes([]byte(m.SignatureAlgorithm)),
	}
	for _, s := range m.PublicSignals {
		e, err := ParseSignal(s)
		if err != nil {
			return nil, err
		}
		extra = append(extra, e)
	}
	return extra, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgRegister) ValidateBasic() error {
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
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
	for what, b := range map[string][]byte{"idc": m.Idc, "pc_anml": m.PcAnml, "pc_erth": m.PcErth} {
		if _, err := Field(what, b); err != nil {
			return err
		}
	}
	if len(m.Affiliate) > MaxAddressBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "affiliate exceeds %d bytes", MaxAddressBytes)
	}
	for what, ct := range map[string][]byte{
		"ciphertext_anml": m.CiphertextAnml, "ciphertext_erth": m.CiphertextErth,
	} {
		if err := checkCiphertext(what, ct); err != nil {
			return err
		}
	}
	return nil
}

// --- MsgClaimAnml --------------------------------------------------------

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgClaimAnml) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgClaimAnml) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: day, pc, Bytes(ciphertext).
func (m *MsgClaimAnml) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := Field("pc", m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.U64(m.Day), pc, privacy.Bytes(m.Ciphertext)}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgClaimAnml) ValidateBasic() error {
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
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

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgSetCaretaker) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgSetCaretaker) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: option_id then percent, per entry.
func (m *MsgSetCaretaker) SighashFields(address.Codec) ([]fr.Element, error) {
	extra := make([]fr.Element, 0, 2*len(m.Percentages))
	for _, w := range m.Percentages {
		extra = append(extra, privacy.U64(w.OptionId), privacy.U64(w.Percent))
	}
	return extra, nil
}

// ValidateBasic checks everything that needs no state. The split itself is
// checked by x/allocation, against the options as they stand.
func (m *MsgSetCaretaker) ValidateBasic() error {
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	if len(m.Percentages) > allocationtypes.MaxVoterOptions {
		return errorsmod.Wrapf(ErrInvalidMsg, "split across %d options exceeds %d", len(m.Percentages), allocationtypes.MaxVoterOptions)
	}
	return m.Membership.ValidateBasic()
}

// --- MsgBindReferrer -----------------------------------------------------

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgBindReferrer) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgBindReferrer) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: Bytes(address bytes) (Bytes of
// nothing to clear).
func (m *MsgBindReferrer) SighashFields(ac address.Codec) ([]fr.Element, error) {
	var bz []byte
	if m.Address != "" {
		var err error
		if bz, err = ac.StringToBytes(m.Address); err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidMsg, "address: %v", err)
		}
	}
	return []fr.Element{privacy.Bytes(bz)}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgBindReferrer) ValidateBasic() error {
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	if len(m.Address) > MaxAddressBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "address exceeds %d bytes", MaxAddressBytes)
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
