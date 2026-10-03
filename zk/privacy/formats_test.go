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

// v2 (value-blind) golden: the goldenKeys esk, rho and rcm, memo "golden memo".
// Cross-checked against Python cryptography (X25519, HKDF-SHA256,
// ChaCha20-Poly1305) when it was recorded.
const goldenBlindCT = "79a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51a8b8d4fe44e9fcb771cba93975cb4507ff1d20e446a6a4cd8336f9a50186a7de58a5b4570c62bfd9cd347f5921103700103da6af3ce492bbd1f936a4310b3b01a1d583847125f7632547dfb2ea23c438f21cd4a419f9ef66d92660af42686e93c890bc37f68cf282f46ca2550ab2df0ce7191a11e7721ce736e0d1bdd62af8be221017ee455ab79e7b2ea0e756a86c39910"

func TestBlindNoteCiphertextGolden(t *testing.T) {
	ek, esk, owner, n := goldenKeys()
	bn := BlindNote{Rho: n.Rho, Rcm: n.Rcm, Memo: n.Memo}
	ct, err := EncryptBlindNote(bn, owner.EKPub, esk)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != BlindNoteCiphertextBytes || BlindNoteCiphertextBytes != 177 {
		t.Fatalf("ciphertext is %d bytes", len(ct))
	}
	if hex.EncodeToString(ct) != goldenBlindCT {
		t.Fatalf("ciphertext %x", ct)
	}
	got, err := DecryptBlindNote(ct, ek)
	if err != nil || got != bn {
		t.Fatalf("decrypt: %v", err)
	}
	// The recipient's acceptance check: with the published asset and value
	// the opening recomputes the note's cm; with any other value it does not.
	cm := n.CM(owner.OwnerPK)
	if CM(n.AssetID, n.Value, got.PC(owner.OwnerPK)) != cm {
		t.Fatal("opening does not recompute cm")
	}
	if CM(n.AssetID, n.Value+1, got.PC(owner.OwnerPK)) == cm {
		t.Fatal("cm check accepts another value")
	}

	// Not ours; tampered; truncated; a v1 ciphertext is not a v2 one; low-order key.
	var other [32]byte
	other[0] = 9
	if _, err := DecryptBlindNote(ct, other); err == nil {
		t.Error("opened with the wrong key")
	}
	bad := append([]byte{}, ct...)
	bad[100] ^= 1
	if _, err := DecryptBlindNote(bad, ek); err == nil {
		t.Error("tampered ciphertext opened")
	}
	if _, err := DecryptBlindNote(ct[:176], ek); err == nil {
		t.Error("truncated ciphertext opened")
	}
	v1, err := EncryptNote(n, cm, owner.EKPub, esk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBlindNote(v1, ek); err == nil {
		t.Error("v1 ciphertext opened as v2")
	}
	if _, err := DecryptNote(append(ct, make([]byte, 40)...), cm, ek); err == nil {
		t.Error("v2 ciphertext opened as v1")
	}
	if _, err := EncryptBlindNote(bn, [32]byte{}, esk); err == nil {
		t.Error("encrypted to the zero point")
	}
}

// Blind stake ciphertext golden: the goldenKeys esk, rho and rcm, memo
// "golden memo", salt "earth.stake.v1", version 0x03. Cross-checked against
// Python cryptography 46 (X25519, HKDF-SHA256, ChaCha20-Poly1305).
const goldenBlindStakeCT = "79a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51aee8945675bfea325467df067f447ff0537e1b1b9afd7e07645f504ccb3e3191db98002dd3d6117d2707071721157989713d23aa9e382fc26cd5c51222610d5fa1f25d4bc3ef9fe7634b319e691e6a1d4d90865917da6b72c6b247658114c4bdf82055ede3b4e2fe90ae1406fac3aceaf280ecf08dd8ce8f3e572daa238d8157fe50fe43d980ffd1b4fc4b9af1526c7b568"

func TestBlindStakeCiphertextGolden(t *testing.T) {
	ek, esk, owner, n := goldenKeys()
	bn := BlindNote{Rho: n.Rho, Rcm: n.Rcm, Memo: n.Memo}
	ct, err := EncryptBlindStakeNote(bn, owner.EKPub, esk)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != BlindStakeCiphertextBytes || BlindStakeCiphertextBytes != 177 {
		t.Fatalf("ciphertext is %d bytes", len(ct))
	}
	if hex.EncodeToString(ct) != goldenBlindStakeCT {
		t.Fatalf("ciphertext %x", ct)
	}
	got, err := DecryptBlindStakeNote(ct, ek)
	if err != nil || got != bn {
		t.Fatalf("decrypt: %v", err)
	}
	// The owner's acceptance check against the published denom and amount.
	asset := AssetID("derth/earthvaloper1golden")
	cm := StakeCM(asset, 1_000_000, StakePC(owner.OwnerPK, n.Rho, n.Rcm))
	if StakeCM(asset, 1_000_000, got.SPC(owner.OwnerPK)) != cm {
		t.Fatal("opening does not recompute the stake cm")
	}
	if StakeCM(asset, 1_000_001, got.SPC(owner.OwnerPK)) == cm {
		t.Fatal("cm check accepts another amount")
	}
	// A note v2 ciphertext is not a stake one, and the reverse.
	v2, err := EncryptBlindNote(bn, owner.EKPub, esk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBlindStakeNote(v2, ek); err == nil {
		t.Error("v2 note ciphertext opened as a stake ciphertext")
	}
	if _, err := DecryptBlindNote(ct, ek); err == nil {
		t.Error("stake ciphertext opened as a v2 note ciphertext")
	}
}
