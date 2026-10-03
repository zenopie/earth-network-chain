package privacy

import (
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Note ciphertexts ("earth note v1"). The chain treats a ciphertext as opaque
// bytes (at most 1024, bound into the signal so a relay cannot swap it); this
// is the layout every wallet and tool writes:
//
//	ct    = epk (32) || ChaCha20-Poly1305(key, nonce = 12 zero bytes, aad = none, pt)
//	key   = HKDF-SHA256(ikm = X25519(esk, ek_pub), salt = "earth.note.v1", info = epk || cm)
//	pt    = 0x01 || asset_id (32) || value (u64 BE) || rho (32) || rcm (32) || memo (64)
//
// 217 bytes for every note. esk is a fresh X25519 key per note, so the fixed
// nonce never repeats under one key; cm (the note's commitment, which the
// chain publishes beside the ciphertext) in info binds the ciphertext to its
// note, so a ciphertext cannot be replayed onto another output. The recipient
// opens it with ek (the secret behind ek_pub) and the cm at that position;
// pc is then recomputed from owner_pk, rho and rcm and cm checked, so a note
// is only accepted if it is really the one in the tree.

const (
	// NoteVersion is the plaintext's leading byte.
	NoteVersion byte = 0x01
	// NoteMemoBytes is the fixed memo length (zero padded).
	NoteMemoBytes = 64
	// NotePlaintextBytes is 1 + 32 + 8 + 32 + 32 + 64.
	NotePlaintextBytes = 1 + 32 + 8 + 32 + 32 + NoteMemoBytes
	// NoteCiphertextBytes is epk + plaintext + the Poly1305 tag.
	NoteCiphertextBytes = 32 + NotePlaintextBytes + chacha20poly1305.Overhead
)

var noteSalt = []byte("earth.note.v1")

// NotePlaintext is a note's opening as its recipient needs it.
type NotePlaintext struct {
	AssetID fr.Element // AssetID(denom)
	Value   uint64
	Rho     fr.Element
	Rcm     fr.Element
	Memo    [NoteMemoBytes]byte
}

// CM is the commitment of this note for owner ownerPK.
func (n NotePlaintext) CM(ownerPK fr.Element) fr.Element {
	return H(TagCM, n.AssetID, U64(n.Value), PC(ownerPK, n.Rho, n.Rcm))
}

func (n NotePlaintext) bytes() []byte {
	b := make([]byte, 0, NotePlaintextBytes)
	b = append(b, NoteVersion)
	b = append(b, FieldBytes(n.AssetID)...)
	b = binary.BigEndian.AppendUint64(b, n.Value)
	b = append(b, FieldBytes(n.Rho)...)
	b = append(b, FieldBytes(n.Rcm)...)
	return append(b, n.Memo[:]...)
}

func parseNotePlaintext(b []byte) (NotePlaintext, error) {
	var n NotePlaintext
	if len(b) != NotePlaintextBytes || b[0] != NoteVersion {
		return n, errors.New("note: unknown plaintext version or length")
	}
	var err error
	if n.AssetID, err = FieldFromBytes(b[1:33]); err != nil {
		return n, err
	}
	n.Value = binary.BigEndian.Uint64(b[33:41])
	if n.Rho, err = FieldFromBytes(b[41:73]); err != nil {
		return n, err
	}
	if n.Rcm, err = FieldFromBytes(b[73:105]); err != nil {
		return n, err
	}
	copy(n.Memo[:], b[105:])
	return n, nil
}

func noteKey(shared, epk []byte, cm fr.Element) ([]byte, error) {
	info := append(append([]byte{}, epk...), FieldBytes(cm)...)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, noteSalt, info), key); err != nil {
		return nil, err
	}
	return key, nil
}

// EKPub is the X25519 public key of the encryption secret ek.
func EKPub(ek [32]byte) ([32]byte, error) {
	var out [32]byte
	pub, err := curve25519.X25519(ek[:], curve25519.Basepoint)
	if err != nil {
		return out, err
	}
	copy(out[:], pub)
	return out, nil
}

// EncryptNote encrypts n, whose commitment is cm, to ekPub with the ephemeral
// secret esk (32 random bytes; deterministic only in fixtures).
func EncryptNote(n NotePlaintext, cm fr.Element, ekPub [32]byte, esk [32]byte) ([]byte, error) {
	epk, err := curve25519.X25519(esk[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(esk[:], ekPub[:])
	if err != nil {
		return nil, fmt.Errorf("note: %w", err) // a low-order ek_pub
	}
	key, err := noteKey(shared, epk, cm)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(epk, make([]byte, chacha20poly1305.NonceSize), n.bytes(), nil), nil
}

// DecryptNote opens ct with the encryption secret ek, for the note whose
// commitment is cm. An error means the note is not ours (or is malformed);
// callers trial-decrypting every output just skip it. The caller must still
// check n.CM(owner_pk) == cm before trusting the opening.
func DecryptNote(ct []byte, cm fr.Element, ek [32]byte) (NotePlaintext, error) {
	if len(ct) != NoteCiphertextBytes {
		return NotePlaintext{}, errors.New("note: wrong ciphertext length")
	}
	epk := ct[:32]
	shared, err := curve25519.X25519(ek[:], epk)
	if err != nil {
		return NotePlaintext{}, fmt.Errorf("note: %w", err)
	}
	key, err := noteKey(shared, epk, cm)
	if err != nil {
		return NotePlaintext{}, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return NotePlaintext{}, err
	}
	pt, err := aead.Open(nil, make([]byte, chacha20poly1305.NonceSize), ct[32:], nil)
	if err != nil {
		return NotePlaintext{}, errors.New("note: not ours")
	}
	return parseNotePlaintext(pt)
}

// ---- v2: value-blind ("earth note v2") ---------------------------------------
//
// A v1 ciphertext needs the note's cm, so its value, when it is made. Notes
// whose value the chain decides when the msg runs cannot have one: a dex
// swap's output (MsgBuyAnml, MsgNoteSwap), an LP withdrawal priced at
// maturity, a derth mint at the live rate. For those the sender encrypts only
// the secrets the recipient cannot learn from the chain:
//
//	ct    = epk (32) || ChaCha20-Poly1305(key, nonce = 12 zero bytes, aad = none, pt)
//	key   = HKDF-SHA256(ikm = X25519(esk, ek_pub), salt = "earth.note.v2", info = epk)
//	pt    = 0x02 || rho (32) || rcm (32) || memo (64)
//
// 177 bytes; the length alone tells v1 (217) from v2. The asset and value are
// what the chain publishes for that note (the mint/shield event's amount at
// the note's position). The recipient opens ct, recomputes
// pc = PC(owner_pk, rho, rcm) and cm = CM(AssetID(denom), value, pc), and
// accepts the note only if cm equals the commitment in the tree. info cannot
// bind cm (the sender does not know it), so binding a v2 ciphertext to its
// note is exactly that cm check: a v2 ciphertext replayed onto another output
// opens but names a pc whose cm does not match, and is dropped. esk is fresh
// per note, so the fixed nonce never repeats under one key.

const (
	// BlindNoteVersion is the v2 plaintext's leading byte.
	BlindNoteVersion byte = 0x02
	// BlindNotePlaintextBytes is 1 + 32 + 32 + 64.
	BlindNotePlaintextBytes = 1 + 32 + 32 + NoteMemoBytes
	// BlindNoteCiphertextBytes is epk + plaintext + the Poly1305 tag.
	BlindNoteCiphertextBytes = 32 + BlindNotePlaintextBytes + chacha20poly1305.Overhead
)

var blindNoteSalt = []byte("earth.note.v2")

// BlindNote is a v2 opening: the note's secrets without its asset or value.
type BlindNote struct {
	Rho  fr.Element
	Rcm  fr.Element
	Memo [NoteMemoBytes]byte
}

// PC is the note's owner commitment for ownerPK.
func (n BlindNote) PC(ownerPK fr.Element) fr.Element { return PC(ownerPK, n.Rho, n.Rcm) }

func (n BlindNote) bytes() []byte {
	b := make([]byte, 0, BlindNotePlaintextBytes)
	b = append(b, BlindNoteVersion)
	b = append(b, FieldBytes(n.Rho)...)
	b = append(b, FieldBytes(n.Rcm)...)
	return append(b, n.Memo[:]...)
}

func blindNoteAEAD(shared, epk []byte) (cipher.AEAD, error) { return blindAEAD(blindNoteSalt, shared, epk) }

func blindAEAD(salt, shared, epk []byte) (cipher.AEAD, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, epk), key); err != nil {
		return nil, err
	}
	return chacha20poly1305.New(key)
}

// EncryptBlindNote encrypts n's secrets to ekPub with the ephemeral secret esk
// (32 random bytes; deterministic only in fixtures).
func EncryptBlindNote(n BlindNote, ekPub [32]byte, esk [32]byte) ([]byte, error) {
	return encryptBlind(blindNoteSalt, n.bytes(), ekPub, esk)
}

func encryptBlind(salt, pt []byte, ekPub [32]byte, esk [32]byte) ([]byte, error) {
	epk, err := curve25519.X25519(esk[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(esk[:], ekPub[:])
	if err != nil {
		return nil, fmt.Errorf("note: %w", err) // a low-order ek_pub
	}
	aead, err := blindAEAD(salt, shared, epk)
	if err != nil {
		return nil, err
	}
	return aead.Seal(epk, make([]byte, chacha20poly1305.NonceSize), pt, nil), nil
}

// DecryptBlindNote opens a v2 ciphertext with the encryption secret ek. An
// error means the note is not ours (or is malformed). The caller must still
// check CM(asset, value, n.PC(owner_pk)) == cm with the asset and value the
// chain published for the note before trusting the opening.
func DecryptBlindNote(ct []byte, ek [32]byte) (BlindNote, error) {
	return decryptBlind(blindNoteSalt, BlindNoteVersion, ct, ek)
}

func decryptBlind(salt []byte, version byte, ct []byte, ek [32]byte) (BlindNote, error) {
	var n BlindNote
	if len(ct) != BlindNoteCiphertextBytes {
		return n, errors.New("note: wrong ciphertext length")
	}
	epk := ct[:32]
	shared, err := curve25519.X25519(ek[:], epk)
	if err != nil {
		return n, fmt.Errorf("note: %w", err)
	}
	aead, err := blindAEAD(salt, shared, epk)
	if err != nil {
		return n, err
	}
	pt, err := aead.Open(nil, make([]byte, chacha20poly1305.NonceSize), ct[32:], nil)
	if err != nil {
		return n, errors.New("note: not ours")
	}
	if len(pt) != BlindNotePlaintextBytes || pt[0] != version {
		return n, errors.New("note: unknown plaintext version or length")
	}
	if n.Rho, err = FieldFromBytes(pt[1:33]); err != nil {
		return n, err
	}
	if n.Rcm, err = FieldFromBytes(pt[33:65]); err != nil {
		return n, err
	}
	copy(n.Memo[:], pt[65:])
	return n, nil
}

// ---- blind stake ciphertext ("earth stake v1") ------------------------------
//
// Every stake note the chain mints (derth at the live rate, an unbond claim,
// a stake vote's re-mint, an unlocked position) has a value the chain
// decides, so its owner's wallet cannot know cm in advance. The msg carries
// the note's secrets encrypted to the owner, exactly as a v2 note does, under
// its own salt and version byte so the two can never be confused:
//
//	ct    = epk (32) || ChaCha20-Poly1305(key, nonce = 12 zero bytes, aad = none, pt)
//	key   = HKDF-SHA256(ikm = X25519(esk, ek_pub), salt = "earth.stake.v1", info = epk)
//	pt    = 0x03 || rho (32) || rcm (32) || memo (64)
//
// 177 bytes. The owner opens it, recomputes spc = H(TAG_SPC, owner_pk, rho,
// rcm) and cm = H(TAG_STAKE, AssetID(denom), amount, spc) with the denom and
// amount the chain publishes for the stake note at that position, and accepts
// it only if cm matches.

const (
	// BlindStakeVersion is the stake ciphertext plaintext's leading byte.
	BlindStakeVersion byte = 0x03
	// BlindStakeCiphertextBytes is the stake ciphertext's length (as v2's).
	BlindStakeCiphertextBytes = BlindNoteCiphertextBytes
)

var blindStakeSalt = []byte("earth.stake.v1")

// SPC is the stake note's owner commitment for ownerPK (StakePC).
func (n BlindNote) SPC(ownerPK fr.Element) fr.Element { return StakePC(ownerPK, n.Rho, n.Rcm) }

func (n BlindNote) stakeBytes() []byte {
	b := n.bytes()
	b[0] = BlindStakeVersion
	return b
}

// EncryptBlindStakeNote encrypts a minted stake note's secrets to ekPub with
// the ephemeral secret esk (32 random bytes; deterministic only in fixtures).
func EncryptBlindStakeNote(n BlindNote, ekPub [32]byte, esk [32]byte) ([]byte, error) {
	return encryptBlind(blindStakeSalt, n.stakeBytes(), ekPub, esk)
}

// DecryptBlindStakeNote opens a stake ciphertext with ek. The caller must
// still check StakeCM(asset, amount, n.SPC(owner_pk)) == cm before trusting
// it.
func DecryptBlindStakeNote(ct []byte, ek [32]byte) (BlindNote, error) {
	return decryptBlind(blindStakeSalt, BlindStakeVersion, ct, ek)
}
