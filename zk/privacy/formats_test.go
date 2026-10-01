package privacy

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Golden inputs: every byte fixed, so the vectors below pin both formats.
// The ciphertext vector was cross-checked against an independent
// implementation (Python cryptography: X25519, HKDF-SHA256,
// ChaCha20-Poly1305) when it was recorded.
func goldenKeys() (ek, esk [32]byte, owner ShieldedAddress, n NotePlaintext) {
	for i := range ek {
		ek[i] = byte(i + 1)
		esk[i] = byte(0x40 + i)
	}
	ekPub, err := EKPub(ek)
	if err != nil {
		panic(err)
	}
	owner = ShieldedAddress{OwnerPK: OwnerPK(U64(7)), EKPub: ekPub}
	n = NotePlaintext{AssetID: AssetID("uerth"), Value: 1_234_567, Rho: U64(11), Rcm: U64(13)}
	copy(n.Memo[:], "golden memo")
	return
}

const (
	goldenEKPub   = "07a37cbc142093c8b755dc1b10e86cb426374ad16aa853ed0bdfc0b2b86d1c7c"
	goldenAddress = "erthz1qy4m4lwe79wu4p4gs6vqrtdhcngph2phnll9x5t296jdd9m5z4p0gpar0j7pggynezm4thqmzr5xedpxxa9dz64g20kshh7qk2ux68rudaur8p"
	goldenCM      = "0ad591ff4e6c10b942740693cc1cbbc471b5f6b11727f4d6252c9dcf47f59a0a"
	goldenCT      = "79a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51ae3683366a28b0db80b20a9c90235b415c7e16c31a8338fd12d041aac85c3a56c2173a66046bec482b582fcfbcf49057b28fd406ae26700ffef383a1de0a2059a9cae640054df4a1945e4bf2b38e7af97b8ee62f4da48cdc145dfbc2f7708edac17b42a06c5048ef7971f7fe2dc7eb63dcc64ff592a8e7c344e0b539c6fdd1af43f0b6c741de6deb755e0d3dcce0a63c37ab0b016f6d707e41cf2e5f458ee1bcf7cdd19087661ba6d6bf670abae5881684f2a2c89ce02b0a4db"
)

func TestShieldedAddressGolden(t *testing.T) {
	_, _, owner, _ := goldenKeys()
	if hex.EncodeToString(owner.EKPub[:]) != goldenEKPub {
		t.Fatalf("ek_pub %x", owner.EKPub)
	}
	s := owner.Encode()
	if s != goldenAddress {
		t.Fatalf("address %s", s)
	}
	if !strings.HasPrefix(s, "erthz1") || len(s) != 116 {
		t.Fatalf("shape: %d chars", len(s))
	}
	for _, in := range []string{s, strings.ToUpper(s)} {
		got, err := DecodeShieldedAddress(in)
		if err != nil || got != owner {
			t.Fatalf("round trip %q: %v", in, err)
		}
	}
}

func TestShieldedAddressRefusals(t *testing.T) {
	_, _, owner, _ := goldenKeys()
	s := owner.Encode()
	flip := []byte(s)
	flip[20] = map[bool]byte{true: 'q', false: 'p'}[flip[20] != 'q']
	mixed := s[:10] + strings.ToUpper(s[10:])
	// Same payload under bech32 (BIP-173) instead of bech32m: refused.
	payload := append([]byte{AddressVersion}, append(FieldBytes(owner.OwnerPK), owner.EKPub[:]...)...)
	data, _ := convertBits(payload, 8, 5, true)
	values := append(bech32HRPExpand(AddressHRP), data...)
	mod := bech32Polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ 1
	var b strings.Builder
	b.WriteString(AddressHRP + "1")
	for _, d := range data {
		b.WriteByte(bech32Charset[d])
	}
	for i := 0; i < 6; i++ {
		b.WriteByte(bech32Charset[(mod>>(5*(5-i)))&31])
	}
	enc := func(hrp string, p []byte) string {
		d, _ := convertBits(p, 8, 5, true)
		return bech32mEncode(hrp, d)
	}
	v2 := append([]byte{0x02}, payload[1:]...)
	nonCanon := append([]byte{AddressVersion}, append(make([]byte, 32), owner.EKPub[:]...)...)
	for i := 1; i < 33; i++ {
		nonCanon[i] = 0xff // > the BN254 modulus
	}
	for name, bad := range map[string]string{
		"checksum":      string(flip),
		"mixed case":    mixed,
		"bech32 (v0)":   b.String(),
		"transparent":   enc("earth", payload),
		"version":       enc(AddressHRP, v2),
		"short":         enc(AddressHRP, payload[:64]),
		"non-canonical": enc(AddressHRP, nonCanon),
	} {
		if _, err := DecodeShieldedAddress(bad); err == nil {
			t.Errorf("%s: accepted %q", name, bad)
		}
	}
}

// BIP-350's own vectors, for the checksum.
func TestBech32mBIP350Vectors(t *testing.T) {
	for _, s := range []string{
		"A1LQFN3A", "a1lqfn3a",
		"an83characterlonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11sg7hg6",
		"abcdef1l7aum6echk45nj3s0wdvt2fg8x9yrzpqzd3ryx",
		"split1checkupstagehandshakeupstreamerranterredcaperredlc445v",
		"?1v759aa",
	} {
		if _, _, err := bech32mDecode(s); err != nil {
			t.Errorf("valid %q: %v", s, err)
		}
	}
	for _, s := range []string{
		"qyrz8wqd2c9m", "1qyrz8wqd2c9m", "y1b0jsk6g", "lt1igcx5c0", "in1muywd", "mm1crxm3i",
		"au1s5cgom", "M1VUXWEZ", "16plkw9", "1p2gdwpf",
		// bech32 (BIP-173) strings: valid there, not here.
		"a12uel5l", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
	} {
		if _, _, err := bech32mDecode(s); err == nil {
			t.Errorf("invalid %q accepted", s)
		}
	}
}

// RFC 7748 §6.1, for EKPub.
func TestX25519RFC7748(t *testing.T) {
	var sk [32]byte
	b, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	copy(sk[:], b)
	pub, err := EKPub(sk)
	if err != nil || hex.EncodeToString(pub[:]) != "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a" {
		t.Fatalf("X25519 public key %x: %v", pub, err)
	}
}

func TestNoteCiphertextGolden(t *testing.T) {
	ek, esk, owner, n := goldenKeys()
	cm := n.CM(owner.OwnerPK)
	if hex.EncodeToString(FieldBytes(cm)) != goldenCM {
		t.Fatalf("cm %x", FieldBytes(cm))
	}
	ct, err := EncryptNote(n, cm, owner.EKPub, esk)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != NoteCiphertextBytes || NoteCiphertextBytes != 217 {
		t.Fatalf("ciphertext is %d bytes", len(ct))
	}
	if hex.EncodeToString(ct) != goldenCT {
		t.Fatalf("ciphertext %x", ct)
	}
	got, err := DecryptNote(ct, cm, ek)
	if err != nil || got != n {
		t.Fatalf("decrypt: %v", err)
	}
	if got.CM(owner.OwnerPK) != cm {
		t.Fatal("opening does not recompute cm")
	}

	// Not ours; bound to its cm; tampered; truncated.
	var other [32]byte
	other[0] = 9
	if _, err := DecryptNote(ct, cm, other); err == nil {
		t.Error("opened with the wrong key")
	}
	if _, err := DecryptNote(ct, U64(1), ek); err == nil {
		t.Error("opened against another output's cm")
	}
	bad := append([]byte{}, ct...)
	bad[100] ^= 1
	if _, err := DecryptNote(bad, cm, ek); err == nil {
		t.Error("tampered ciphertext opened")
	}
	if _, err := DecryptNote(ct[:216], cm, ek); err == nil {
		t.Error("truncated ciphertext opened")
	}
	// A low-order ek_pub is refused, not encrypted to.
	if _, err := EncryptNote(n, cm, [32]byte{}, esk); err == nil {
		t.Error("encrypted to the zero point")
	}
}
