package types

import (
	"fmt"
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
	_ shieldedtypes.PrivateMsg = (*MsgBindHandle)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgMoveHandle)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgMoveCaretaker)(nil)

	_ sdk.HasValidateBasic = (*MsgRegister)(nil)
	_ sdk.HasValidateBasic = (*MsgClaimAnml)(nil)
	_ sdk.HasValidateBasic = (*MsgSetCaretaker)(nil)
	_ sdk.HasValidateBasic = (*MsgBindHandle)(nil)
	_ sdk.HasValidateBasic = (*MsgMoveHandle)(nil)
	_ sdk.HasValidateBasic = (*MsgMoveCaretaker)(nil)
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

// checkCiphertext checks a minted note's ciphertext: the amount-blind v2
// ciphertext every note the chain mints carries (registration rewards, ANML
// claims), exactly shieldedtypes.BlindCiphertextBytes.
func checkCiphertext(what string, b []byte) error {
	if err := shieldedtypes.CheckBlindCiphertext(what, b); err != nil {
		return errorsmod.Wrap(ErrInvalidMsg, err.Error())
	}
	return nil
}

// ValidateBasic checks a membership proof's shape.
func (m Membership) ValidateBasic() error {
	if err := shieldedtypes.CheckProofLength(m.Proof); err != nil {
		return errorsmod.Wrap(ErrInvalidMembership, err.Error())
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
// max_activation, max_predecessor.
func MembershipPublicInputs(m Membership, scope, signal, excludedDsc, excludedCountry fr.Element, maxActivation, maxPredecessor uint64) [][]byte {
	return [][]byte{
		m.Root,
		privacy.FieldBytes(scope),
		m.Nullifier,
		privacy.FieldBytes(signal),
		privacy.FieldBytes(excludedDsc),
		privacy.FieldBytes(excludedCountry),
		privacy.FieldBytes(privacy.U64(maxActivation)),
		privacy.FieldBytes(privacy.U64(maxPredecessor)),
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
// signal: 0 when the registration names no referrer, and
// privacy.AffiliateField(affiliate_handle) = H(TAG_AFFILIATE,
// Bytes(affiliate_handle)) when it does. The referral note is the chain's to
// make (to the handle's registered address), so the handle is all it binds.
func (m *MsgRegister) AffiliateField() (fr.Element, error) {
	if m.AffiliateHandle == "" {
		return fr.Element{}, nil
	}
	if err := ValidateHandle(m.AffiliateHandle); err != nil {
		return fr.Element{}, errorsmod.Wrapf(ErrInvalidMsg, "affiliate_handle: %v", err)
	}
	return privacy.AffiliateField(m.AffiliateHandle), nil
}

// canonicalBytes decodes addr and requires it to be the canonical
// (lowercase) encoding of its bytes. A proof binds the bytes, not the
// string: an uppercase re-spelling by whoever relays the tx would otherwise
// pass as the same tx under another hash and be stored as given.
func canonicalBytes(ac address.Codec, addr string) ([]byte, error) {
	bz, err := ac.StringToBytes(addr)
	if err != nil {
		return nil, err
	}
	if s, err := ac.BytesToString(bz); err != nil || s != addr {
		return nil, fmt.Errorf("%q is not the canonical encoding of its address", addr)
	}
	return bz, nil
}

// Binding is the value the passport proof's address input must carry on
// chain chainID: zk/privacy.RegistrationBinding(chain_id, idc, pc_anml,
// ciphertext_anml, pc_erth, ciphertext_erth, AffiliateField).
func (m *MsgRegister) Binding(ac address.Codec, chainID string) (fr.Element, error) {
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
	aff, err := m.AffiliateField()
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.RegistrationBinding(chainID, idc, pcAnml, m.CiphertextAnml, pcErth, m.CiphertextErth, aff), nil
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
	aff, err := m.AffiliateField()
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
	if err := shieldedtypes.CheckProofLength(m.Proof); err != nil {
		return errorsmod.Wrap(ErrInvalidMsg, err.Error())
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
	if _, err := m.AffiliateField(); err != nil {
		return err
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

// --- MsgBindHandle -------------------------------------------------------

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgBindHandle) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgBindHandle) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// ShieldedAddress is the address the handle is bound to; ok false for a
// release (empty address).
func (m *MsgBindHandle) ShieldedAddress() (privacy.ShieldedAddress, bool, error) {
	if m.Address == "" {
		return privacy.ShieldedAddress{}, false, nil
	}
	a, err := privacy.DecodeShieldedAddress(m.Address)
	if err != nil {
		return a, false, errorsmod.Wrapf(ErrInvalidMsg, "address: %v", err)
	}
	if a.Encode() != m.Address {
		return a, false, errorsmod.Wrap(ErrInvalidMsg, "address is not in its canonical (lowercase) form")
	}
	return a, true, nil
}

// SighashFields implements PrivateMsg: Bytes(handle), owner_pk,
// Bytes(ek_pub) (Bytes of nothing, 0 and Bytes of nothing for a release).
func (m *MsgBindHandle) SighashFields(address.Codec) ([]fr.Element, error) {
	a, ok, err := m.ShieldedAddress()
	if err != nil {
		return nil, err
	}
	var ek []byte
	if ok {
		ek = a.EKPub[:]
	}
	return []fr.Element{privacy.Bytes([]byte(m.Handle)), a.OwnerPK, privacy.Bytes(ek)}, nil
}

// ValidateBasic checks everything that needs no state: a handle and an
// address (a bind), or neither (a release).
func (m *MsgBindHandle) ValidateBasic() error {
	if len(m.Address) > MaxShieldedAddressBytes {
		return errorsmod.Wrapf(ErrInvalidMsg, "address exceeds %d bytes", MaxShieldedAddressBytes)
	}
	if (m.Handle == "") != (m.Address == "") {
		return errorsmod.Wrap(ErrInvalidMsg, "a bind names a handle and an address; a release neither")
	}
	if m.Handle != "" {
		if err := ValidateHandle(m.Handle); err != nil {
			return errorsmod.Wrapf(ErrInvalidMsg, "handle: %v", err)
		}
		if _, _, err := m.ShieldedAddress(); err != nil {
			return err
		}
	}
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// --- MsgMoveCaretaker ----------------------------------------------------

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgMoveCaretaker) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgMoveCaretaker) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: new_owner.
func (m *MsgMoveCaretaker) SighashFields(address.Codec) ([]fr.Element, error) {
	owner, err := Field("new_owner", m.NewOwner)
	if err != nil {
		return nil, err
	}
	return []fr.Element{owner}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgMoveCaretaker) ValidateBasic() error {
	if _, err := Field("new_owner", m.NewOwner); err != nil {
		return err
	}
	if string(m.NewOwner) == string(m.Membership.Nullifier) {
		return errorsmod.Wrap(ErrInvalidMsg, "new_owner is the prover")
	}
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// --- MsgMoveHandle -------------------------------------------------------

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgMoveHandle) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgMoveHandle) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: Bytes(handle), new_owner.
func (m *MsgMoveHandle) SighashFields(address.Codec) ([]fr.Element, error) {
	owner, err := Field("new_owner", m.NewOwner)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes([]byte(m.Handle)), owner}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgMoveHandle) ValidateBasic() error {
	if err := ValidateHandle(m.Handle); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "handle: %v", err)
	}
	if _, err := Field("new_owner", m.NewOwner); err != nil {
		return err
	}
	if string(m.NewOwner) == string(m.Membership.Nullifier) {
		return errorsmod.Wrap(ErrInvalidMsg, "new_owner is the prover")
	}
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// MaxShieldedAddressBytes bounds a shielded address string in a msg (one is
// 116 characters).
const MaxShieldedAddressBytes = 128

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
	// seconds. Negative clamps to 0: nobody qualifies. NoBound: any.
	MaxActivation int64
	// MaxPredecessor is the latest predecessor_at a leaf may carry (0: a
	// passport never registered before). Negative clamps to 0: only fresh
	// registrants qualify. NoBound: any.
	MaxPredecessor int64
}

// NoBound is the max_activation / max_predecessor that bounds nothing:
// 2^63 - 1, as a membership public input 0x7fffffffffffffff.
const NoBound int64 = 1<<63 - 1

// BoundInput is a statement bound as the circuit's u64 public input
// (negative: 0).
func BoundInput(b int64) uint64 {
	if b < 0 {
		return 0
	}
	return uint64(b)
}
