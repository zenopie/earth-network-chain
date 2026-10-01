package privacy

import (
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
