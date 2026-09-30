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
	TagVote  = tag("earth.vote")
	TagReg   = tag("earth.reg")
	TagAsset = tag("earth.asset")
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

// IdentityLeaf is H(TAG_LEAF, idc, dsc_key, activated_at), computed by the chain.
func IdentityLeaf(idc, dscKey fr.Element, activatedAt uint64) fr.Element {
	return H(TagLeaf, idc, dscKey, U64(activatedAt))
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
	in := []fr.Element{TagAsset, U64(uint64(len(b)))}
	for i := 0; i < len(b); i += 31 {
		j := min(i+31, len(b))
		var c fr.Element
		c.SetBigInt(new(big.Int).SetBytes(b[i:j]))
		in = append(in, c)
	}
	return H(in...)
}
