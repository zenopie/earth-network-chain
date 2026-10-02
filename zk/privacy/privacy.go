// Package privacy holds the domain-tagged Poseidon2 derivations shared by the
// chain and the Noir privacy circuits (earth-network-mobile/circuits/
// privacy_core). Every function here must match its Noir twin bit for bit: the
// chain computes identity leaves and note commitments, the circuits recompute
// them in-proof, and any divergence makes every proof fail (or, worse, makes
// two different preimages collide on one side only).
//
// Tags are ASCII strings read as big-endian integers ("earth.id" ->
// 0x65617274682e6964). They are distinct from one another and from every small
// integer the passport circuits use as a tag (curve tags 1..7). The sponge
// also absorbs the input length, so hashes of different arity never collide.
// Append only; never change a value.
package privacy

import (
	"errors"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/poseidon2"
)

var (
	TagID    = tag("earth.id")
	TagOwner = tag("earth.owner")
	TagLeaf  = tag("earth.leaf")
	TagSN    = tag("earth.sn")
	TagPC    = tag("earth.pc")
	TagCM    = tag("earth.cm")
	TagNF    = tag("earth.nf")
	TagVote  = tag("earth.vote") // retired (note_vote); never reuse
	TagReg   = tag("earth.reg")
	TagAsset = tag("earth.asset")

	// Chain-side only: no circuit computes these. They define the public
	// `signal` input the circuits bind (see Signal).
	TagSignal = tag("earth.signal")
	TagBytes  = tag("earth.bytes")
	// TagScope domain-separates membership scopes (see Scope).
	TagScope = tag("earth.scope")
)

func tag(s string) fr.Element {
	var e fr.Element
	e.SetBigInt(new(big.Int).SetBytes([]byte(s)))
	return e
}

// H is Poseidon2 over the given elements (the circuit's Poseidon2::hash).
func H(in ...fr.Element) fr.Element { return poseidon2.Hash(in) }

// U64 lifts a uint64 into the field.
func U64(v uint64) fr.Element {
	var e fr.Element
	e.SetUint64(v)
	return e
}

// IDC is the identity commitment H(TAG_ID, id_secret).
func IDC(idSecret fr.Element) fr.Element { return H(TagID, idSecret) }

// OwnerPK is H(TAG_OWNER, nk).
func OwnerPK(nk fr.Element) fr.Element { return H(TagOwner, nk) }

// IdentityLeaf is H(TAG_LEAF, idc, dsc_key, country, activated_at), computed
// by the chain. country is CountryField of the registration's issuing country.
func IdentityLeaf(idc, dscKey, country fr.Element, activatedAt uint64) fr.Element {
	return H(TagLeaf, idc, dscKey, country, U64(activatedAt))
}

// CountryField encodes an ISO 3166-1 alpha-2 code for the identity leaf and
// the membership circuit's excluded_country: its two ASCII bytes read
// big-endian ("DE" -> 0x4445, privacy_core::country_code). Anything that is
// not two uppercase letters, including "" (the issuing CSCA names no country),
// is 0: unknown. 0 is also "exclude no country" in the circuit, so a leaf of
// unknown country can be excluded only by its DSC.
func CountryField(cc string) fr.Element {
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return fr.Element{}
	}
	return U64(uint64(cc[0])<<8 | uint64(cc[1]))
}

// ScopeNullifier is H(TAG_SN, id_secret, scope).
func ScopeNullifier(idSecret, scope fr.Element) fr.Element { return H(TagSN, idSecret, scope) }

// PC is the hidden-owner commitment H(TAG_PC, owner_pk, rho, rcm).
func PC(ownerPK, rho, rcm fr.Element) fr.Element { return H(TagPC, ownerPK, rho, rcm) }

// CM is the note commitment H(TAG_CM, asset, value, pc).
func CM(asset fr.Element, value uint64, pc fr.Element) fr.Element {
	return H(TagCM, asset, U64(value), pc)
}

// NF is the note nullifier H(TAG_NF, nk, rho, position).
func NF(nk, rho fr.Element, position uint32) fr.Element {
	return H(TagNF, nk, rho, U64(uint64(position)))
}

// AssetID maps a bank denom to its in-circuit asset field:
// H(TAG_ASSET, len(denom), c_0, ..., c_k) where c_i are the denom's bytes in
// 31-byte big-endian chunks (so each chunk is below the modulus). The length
// both prefixes the chunks and sets the sponge IV, so no two denoms share an
// encoding. uerth's value is hard-coded in privacy_core as ASSET_ERTH.
func AssetID(denom string) fr.Element {
	b := []byte(denom)
	return H(append([]fr.Element{TagAsset, U64(uint64(len(b)))}, chunks31(b)...)...)
}

// chunks31 splits b into 31-byte big-endian field elements (each below the
// modulus), the encoding AssetID uses.
func chunks31(b []byte) []fr.Element {
	out := make([]fr.Element, 0, (len(b)+30)/31)
	for i := 0; i < len(b); i += 31 {
		j := min(i+31, len(b))
		var c fr.Element
		c.SetBigInt(new(big.Int).SetBytes(b[i:j]))
		out = append(out, c)
	}
	return out
}

// Bytes commits to an arbitrary byte string:
// H(TAG_BYTES, len(b), c_0, ..., c_k), c_i the 31-byte big-endian chunks. The
// length is absorbed twice (as an input and in the sponge IV), so strings
// differing only in trailing zero bytes do not collide. Bytes(nil) =
// H(TAG_BYTES, 0).
func Bytes(b []byte) fr.Element {
	return H(append([]fr.Element{TagBytes, U64(uint64(len(b)))}, chunks31(b)...)...)
}

// Signal is the value a proof's public `signal` input carries for a msg:
//
//	signal = H(TAG_SIGNAL, Bytes(msg_type), Bytes(chain_id), fields...)
//
// A private msg's sighash (zk/orchard.Sighash) is Signal over its type URL,
// chain id, bundle count, bundle digests and own fields: the value its action
// proofs and binding signatures bind, and its membership proof's signal.
//
// msg_type is the enclosing msg's type URL ("/earth.shielded.v1.MsgSend"),
// so one proof can never be replayed as a different kind of msg; chain_id
// stops a proof crossing between networks that share a tree prefix. fields are
// the msg's own values the proof must not be separable from (receiver,
// ciphertexts, min_out, validator, ...), each defined by that msg. The circuit
// treats signal as opaque: it only binds it, so the chain and the wallet
// compute it outside the circuit and any change to a bound field changes the
// public input and fails verification.
func Signal(msgType, chainID string, fields ...fr.Element) fr.Element {
	in := make([]fr.Element, 0, 3+len(fields))
	in = append(in, TagSignal, Bytes([]byte(msgType)), Bytes([]byte(chainID)))
	return H(append(in, fields...)...)
}

// Scope is a membership proof's public scope: H(TAG_SCOPE, Bytes(kind),
// args...). A person's nullifier H(TAG_SN, id_secret, scope) is the same for
// every proof in one scope (so a second claim, vote or split in it is
// recognised, and replaces or is refused) and unlinkable across scopes. The
// circuit takes the scope as an opaque field; these definitions are the
// chain's and the wallet's.
func Scope(kind string, args ...fr.Element) fr.Element {
	return H(append([]fr.Element{TagScope, Bytes([]byte(kind))}, args...)...)
}

// ClaimScope is the ANML claim scope for UTC day `day` (unix seconds / 86400).
func ClaimScope(day uint64) fr.Element { return Scope("claim", U64(day)) }

// CaretakerScope is the one scope of caretaker splits: a person's split is
// always filed under the same nullifier, so a refresh replaces it.
func CaretakerScope() fr.Element { return Scope("caretaker") }

// ReferrerScope is the one scope of referrer bindings: a person's binding is
// always filed under the same nullifier, so a rebind moves it.
func ReferrerScope() fr.Element { return Scope("referrer") }

// GasScope is the scope of the transparent-ERTH gas grant for calendar month
// month, written YYYYMM as a number (October 2026 = 202610, UTC). The gas
// backend pays one grant per nullifier per month; nothing on chain records
// it. A month number, not a day, so a person's grants in one month share a
// nullifier and are refused after the first.
func GasScope(month uint64) fr.Element { return Scope("gas", U64(month)) }

// GasTransparentSignalType is the msg_type a transparent gas grant's
// membership proof binds in place of a type URL: the grant is not a chain
// msg, so it gets a name no type URL can take (they start with "/").
const GasTransparentSignalType = "earth.gas.transparent"

// GasTransparentSignal is the membership signal of a transparent gas grant
// to the account with raw address bytes addr:
//
//	signal = H(TAG_SIGNAL, Bytes("earth.gas.transparent"), Bytes(chain_id), Bytes(addr))
//
// Binding the address means whoever relays the proof cannot redirect the
// grant to themselves.
func GasTransparentSignal(chainID string, addr []byte) fr.Element {
	return Signal(GasTransparentSignalType, chainID, Bytes(addr))
}

// ProposalScope is the assembly ballot on x/gov proposal id in voting round
// round (0, or 1 after the chamber demoted an expedited proposal).
func ProposalScope(proposalID, round uint64) fr.Element {
	return Scope("proposal", U64(proposalID), U64(round))
}

// RemovalScope is the assembly ballot with id ballotID on removing a
// groundworks option.
func RemovalScope(ballotID uint64) fr.Element { return Scope("removal", U64(ballotID)) }

// ProposeRemovalScope is the scope of opening a removal ballot on option
// optionID on UTC day `day`. Nothing records its nullifier; the day only keeps
// one person's proposals on different days unlinkable.
func ProposeRemovalScope(optionID, day uint64) fr.Element {
	return Scope("propose_removal", U64(optionID), U64(day))
}

// RegistrationBinding is what a passport proof's `address` public input
// carries for MsgRegister: the identity commitment the chain will put in the
// tree, the notes it will pay, and the referrer. affiliate is
// Bytes(referrer's address bytes), or 0 for none; bound so that whoever
// relays a registration cannot redirect the referral half to themselves.
//
//	address = H(TAG_REG, idc, pc_anml, pc_erth, affiliate)
func RegistrationBinding(idc, pcAnml, pcErth, affiliate fr.Element) fr.Element {
	return H(TagReg, idc, pcAnml, pcErth, affiliate)
}

// ErrNonCanonical is returned for a 32-byte string that is not a reduced
// BN254 scalar.
var ErrNonCanonical = errors.New("not a canonical 32-byte field element")

// FieldBytes is e as 32 big-endian bytes (the circuits' public-input layout).
func FieldBytes(e fr.Element) []byte {
	b := e.Bytes()
	return b[:]
}

// FieldFromBytes parses exactly 32 big-endian bytes holding a value below the
// modulus. Anything else is refused rather than reduced, so every field value
// has one byte encoding and byte-keyed sets (nullifiers, roots) cannot be
// entered twice under two spellings.
func FieldFromBytes(b []byte) (fr.Element, error) {
	var e fr.Element
	if len(b) != fr.Bytes {
		return e, ErrNonCanonical
	}
	if new(big.Int).SetBytes(b).Cmp(fr.Modulus()) >= 0 {
		return e, ErrNonCanonical
	}
	e.SetBytes(b)
	return e, nil
}
