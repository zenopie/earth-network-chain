package certs

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/poseidon2"
)

// CurveTag identifies the algorithm that produced a public key, absorbed into
// the DSC commitment ahead of the key bytes so that two keys from different
// curves cannot hash alike.
//
// Values are consensus. They are absorbed into a hash the chain compares against
// a circuit's public output, so they must match poa_core::CURVE_TAG_* in
// earth-network-mobile/circuits/poa_core/src/lib.nr exactly and for ever.
// Renumbering one is a registration-breaking change, not a refactor. Append
// only.
type CurveTag uint64

const (
	// TagUnknown is never absorbed: CurveTagOf returns an error instead, so an
	// unrecognised curve fails closed rather than silently sharing tag 0 with
	// every other unrecognised curve — which is the exact ambiguity this whole
	// mechanism exists to remove.
	TagUnknown CurveTag = 0

	TagP256 CurveTag = 1
	TagP384 CurveTag = 2
	TagP521 CurveTag = 3

	TagBrainpoolP256r1 CurveTag = 4
	TagBrainpoolP384r1 CurveTag = 5
	TagBrainpoolP512r1 CurveTag = 6

	// TagRSA covers every modulus size. Unlike the EC curves, RSA keys of
	// different strengths already differ in length, and length is separated by
	// the sponge (see DscCommitment), so one tag is enough to hold RSA apart
	// from the curves.
	TagRSA CurveTag = 7
)

// curveTags maps a parsed curve to its consensus tag. Keyed by the same name
// string the curve tables in curves.go use.
var curveTags = map[string]CurveTag{
	"P-256":           TagP256,
	"P-384":           TagP384,
	"P-521":           TagP521,
	"brainpoolP256r1": TagBrainpoolP256r1,
	"brainpoolP384r1": TagBrainpoolP384r1,
	"brainpoolP512r1": TagBrainpoolP512r1,
}

// CurveTagOf reports the tag for a parsed public key.
//
// It fails rather than defaulting. A key whose curve has no tag cannot be given
// a commitment that is guaranteed distinct from another curve's, and quietly
// assigning one anyway would reintroduce the collision for the curves nobody
// has enumerated yet.
func (pk *PublicKey) CurveTagOf() (CurveTag, error) {
	if pk.IsRSA {
		return TagRSA, nil
	}
	if pk.Curve == nil {
		return TagUnknown, fmt.Errorf("public key has no curve")
	}
	return CurveTagByName(pk.Curve.Name)
}

// CurveTagByName is the same lookup keyed by curve name, for callers that hold
// a curve rather than a parsed certificate — notably tools/poafixtures, which
// generates keys directly and must tag them exactly as the chain would.
func CurveTagByName(name string) (CurveTag, error) {
	tag, ok := curveTags[name]
	if !ok {
		return TagUnknown, fmt.Errorf("curve %q has no commitment tag", name)
	}
	return tag, nil
}

// DscCommitment is the public identity of a Document Signer inside the register
// circuits: Poseidon2 over a curve tag followed by the canonical public-key
// bytes, one field element per byte (ECDSA: x‖y; RSA: modulus big-endian).
//
// The circuit computes this from the key it verified the SOD signature against
// and returns it as a public output; the chain recomputes it from the
// certificate in MsgRegister and requires the two to match. Both sides must
// agree byte-for-byte, so this is the single definition — see
// poa_core::dsc_commitment for the in-circuit twin.
//
// Why the tag is there. The sponge absorbs len<<64 into its capacity slot, so
// inputs of different lengths are already separated and RSA — 256 or 512 bytes
// — collides with nothing. What length does not separate is two curves whose
// coordinates are the same width: P-256 against brainpoolP256r1 (64 bytes each)
// and P-384 against brainpoolP384r1 (96 each). Without a tag those pairs share
// a commitment space, and the commitment is not a label but an identity: it is
// what Registration.dsc_key stores, what RegCountByDsc rate-limits on, and what
// RevokedDscCommitments keys, so a collision would let revoking one signer
// retire another's registrations.
//
// Not exploitable — it needs two certificates a trusted CSCA actually signed
// whose coordinates coincide — which is why this is domain separation done
// while it is cheap rather than an incident.
func DscCommitment(tag CurveTag, pubkey []byte) fr.Element {
	elems := make([]fr.Element, len(pubkey)+1)
	elems[0].SetUint64(uint64(tag))
	for i, b := range pubkey {
		elems[i+1].SetUint64(uint64(b))
	}
	return poseidon2.Hash(elems)
}

// DscCommitmentOf is the form callers should reach for: it derives the tag from
// the key rather than letting a caller pair a tag with the wrong bytes.
func DscCommitmentOf(pk *PublicKey) (fr.Element, error) {
	tag, err := pk.CurveTagOf()
	if err != nil {
		return fr.Element{}, err
	}
	return DscCommitment(tag, pk.CanonicalBytes()), nil
}
