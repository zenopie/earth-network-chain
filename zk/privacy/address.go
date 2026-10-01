package privacy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// A shielded address is what a sender needs to pay a note to its owner:
// owner_pk (the note's pc commits to it) and ek_pub (the note's ciphertext is
// encrypted to it). The chain never parses one — it sees only pcs — but every
// wallet, tool and test must agree on the text form, so it is defined once,
// here:
//
//	bech32m(hrp "erthz", 8-to-5-bit(0x01 || owner_pk (32) || ek_pub (32)))
//
// with the BIP-350 checksum and without BIP-173's 90-character cap (the
// address is 116 characters; the cap exists for error *location*, and the
// checksum still detects errors at this length). owner_pk is a canonical
// BN254 scalar, big-endian; ek_pub an X25519 public key. 0x01 is the
// version byte: a decoder refuses any other.

const (
	// AddressHRP is the shielded address's human-readable part, distinct
	// from the transparent "earth".
	AddressHRP = "erthz"
	// AddressVersion is the one payload version defined.
	AddressVersion byte = 0x01
)

// ShieldedAddress is an owner key and an encryption key.
type ShieldedAddress struct {
	OwnerPK fr.Element
	EKPub   [32]byte
}

// Encode is the address's canonical text form.
func (a ShieldedAddress) Encode() string {
	payload := make([]byte, 0, 65)
	payload = append(payload, AddressVersion)
	payload = append(payload, FieldBytes(a.OwnerPK)...)
	payload = append(payload, a.EKPub[:]...)
	data, _ := convertBits(payload, 8, 5, true)
	return bech32mEncode(AddressHRP, data)
}

func (a ShieldedAddress) String() string { return a.Encode() }

// DecodeShieldedAddress parses an address: hrp "erthz", a valid bech32m
// checksum, all one case, version 0x01, exactly 65 payload bytes with no
// stray padding, and a canonical owner_pk.
func DecodeShieldedAddress(s string) (ShieldedAddress, error) {
	hrp, data, err := bech32mDecode(s)
	if err != nil {
		return ShieldedAddress{}, err
	}
	if hrp != AddressHRP {
		return ShieldedAddress{}, fmt.Errorf("shielded address: hrp %q, want %q", hrp, AddressHRP)
	}
	payload, err := convertBits(data, 5, 8, false)
	if err != nil {
		return ShieldedAddress{}, fmt.Errorf("shielded address: %w", err)
	}
	if len(payload) != 65 {
		return ShieldedAddress{}, fmt.Errorf("shielded address: %d payload bytes, want 65", len(payload))
	}
	if payload[0] != AddressVersion {
		return ShieldedAddress{}, fmt.Errorf("shielded address: unknown version %#x", payload[0])
	}
	pk, err := FieldFromBytes(payload[1:33])
	if err != nil {
		return ShieldedAddress{}, fmt.Errorf("shielded address: owner_pk: %w", err)
	}
	var a ShieldedAddress
	a.OwnerPK = pk
	copy(a.EKPub[:], payload[33:])
	return a, nil
}

// ---- BIP-350 bech32m, no length cap -----------------------------------------

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

const bech32mConst = 0x2bc830a3

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	out := make([]byte, 0, 2*len(hrp)+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

func bech32mEncode(hrp string, data []byte) string {
	values := append(bech32HRPExpand(hrp), data...)
	mod := bech32Polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ bech32mConst
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, d := range data {
		sb.WriteByte(bech32Charset[d])
	}
	for i := 0; i < 6; i++ {
		sb.WriteByte(bech32Charset[(mod>>(5*(5-i)))&31])
	}
	return sb.String()
}

func bech32mDecode(s string) (string, []byte, error) {
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return "", nil, errors.New("bech32m: mixed case")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, errors.New("bech32m: malformed separator or checksum")
	}
	hrp := s[:pos]
	for i := 0; i < len(hrp); i++ {
		if hrp[i] < 33 || hrp[i] > 126 {
			return "", nil, errors.New("bech32m: invalid hrp character")
		}
	}
	data := make([]byte, 0, len(s)-pos-1)
	for i := pos + 1; i < len(s); i++ {
		d := strings.IndexByte(bech32Charset, s[i])
		if d < 0 {
			return "", nil, fmt.Errorf("bech32m: invalid character %q", s[i])
		}
		data = append(data, byte(d))
	}
	if bech32Polymod(append(bech32HRPExpand(hrp), data...)) != bech32mConst {
		return "", nil, errors.New("bech32m: bad checksum")
	}
	return hrp, data[:len(data)-6], nil
}

// convertBits regroups data from fromBits-wide to toBits-wide values. Without
// pad, leftover bits must be fewer than fromBits and all zero.
func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	var acc, bits uint
	maxv := uint(1)<<toBits - 1
	out := make([]byte, 0, len(data)*int(fromBits)/int(toBits)+1)
	for _, b := range data {
		if uint(b)>>fromBits != 0 {
			return nil, errors.New("invalid data range")
		}
		acc = acc<<fromBits | uint(b)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(toBits-bits)&maxv))
		}
	} else if bits >= fromBits || acc<<(toBits-bits)&maxv != 0 {
		return nil, errors.New("invalid padding")
	}
	return out, nil
}
