// Command poafixtures generates witness inputs (Prover.toml) for the passport
// register circuits, plus the CSCA/DSC certificate chain that the chain verifies
// alongside the resulting proof.
//
// It exists because the register circuits and the chain must agree bit-for-bit on
// three things that are easy to get subtly wrong: the DG1→eContent→signedAttrs
// hash binding, the low-s ECDSA normalisation the Noir verifiers require, and the
// Poseidon2 commitment to the Document Signer's public key. Generating the
// witness here — with the chain's own Poseidon2 and certificate parser — makes a
// mismatch fail loudly at fixture time instead of silently at registration.
//
//	go run ./tools/poafixtures <variant> <outdir>
//
// Then, from the circuits workspace:
//
//	nargo execute --package <variant>
//
// The circuit derives dsc_key itself and returns it, so it is not a witness
// input; the value printed here is what the proof's public signals must contain.
//
//	bb prove -b target/<variant>.json -w target/<variant>.gz -o <outdir> -t noir-recursive
//	bb write_vk -b target/<variant>.json -o <outdir> -t noir-recursive
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/poseidon2"
)

const (
	dg1Max         = 95
	eContentMax    = 200
	signedAttrsMax = 200
	currentDate    = 250101 // 2025-01-01, matching the existing fixtures
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: poafixtures <variant> <outdir>")
		os.Exit(2)
	}
	name, outDir := os.Args[1], os.Args[2]
	v, ok := variants()[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown variant %q\n", name)
		os.Exit(2)
	}
	if err := run(v, outDir); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(v variant, outDir string) error {
	// --- passport data -------------------------------------------------------

	dg1 := buildDG1()
	dg1Len := 5 + 88

	// eContent embeds sha256(dg1) at a known offset; signedAttrs embeds
	// sha256(eContent). The circuit re-derives both and asserts the placements,
	// which is what ties the signature to this exact DG1.
	dg1Hash := sha256.Sum256(dg1[:dg1Len])
	eContent, eContentLen, dg1HashOffset := embed(dg1Hash[:], 40)

	eContentHash := sha256.Sum256(eContent[:eContentLen])
	signedAttrs, signedAttrsLen, eContentHashOffset := embed(eContentHash[:], 24)

	msgHash := sha256.Sum256(signedAttrs[:signedAttrsLen])

	// --- Document Signer key + signature ------------------------------------
	//
	// The witness layout differs per key type: P-256 hands Noir's std verifier a
	// combined r‖s, the other curves hand noir-ecdsa separate r and s, and RSA
	// hands noir-bignum 120-bit limbs plus a Barrett parameter.

	keyInputs := map[string][]string{}
	var canonical []byte
	var certPub any
	var certKey any

	if v.ec != nil {
		c := v.ec.curve
		d, pubPt, err := c.generateKey()
		if err != nil {
			return err
		}
		r, sv, err := c.sign(d, msgHash[:])
		if err != nil {
			return err
		}
		xb := padLeft(pubPt.x, v.ec.coordLen)
		yb := padLeft(pubPt.y, v.ec.coordLen)
		canonical = append(append([]byte{}, xb...), yb...)

		keyInputs["dsc_pubkey_x"] = byteStrings(xb)
		keyInputs["dsc_pubkey_y"] = byteStrings(yb)
		if v.ec.combinedSig {
			keyInputs["sod_signature"] = byteStrings(append(padLeft(r, v.ec.coordLen), padLeft(sv, v.ec.coordLen)...))
		} else {
			keyInputs["sod_signature_r"] = byteStrings(padLeft(r, v.ec.coordLen))
			keyInputs["sod_signature_s"] = byteStrings(padLeft(sv, v.ec.coordLen))
		}
		if k, pub := ecdsaKeyFor(c, d, pubPt); pub != nil {
			certKey, certPub = k, pub
		}
	} else {
		key, err := rsa.GenerateKey(rand.Reader, v.rsa.bits)
		if err != nil {
			return err
		}
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, msgHash[:])
		if err != nil {
			return err
		}
		n := key.N
		canonical = n.Bytes()
		keyInputs["dsc_modulus"] = limbs(n, v.rsa.limbs)
		keyInputs["dsc_redc"] = limbs(barrettRedc(n, v.rsa.bits), v.rsa.limbs)
		keyInputs["sod_signature"] = limbs(new(big.Int).SetBytes(sig), v.rsa.limbs)
		certKey, certPub = key, &key.PublicKey
	}

	dscKey := commitment(canonical)

	// --- certificates --------------------------------------------------------

	// Brainpool cannot be certificate-encoded by crypto/x509 (see ecdsaKeyFor),
	// so those variants ship witness + proof only.
	var cscaDER, dscDER []byte
	if certPub != nil {
		var err error
		cscaDER, dscDER, err = makeChain(certKey, certPub)
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if cscaDER != nil {
		if err := os.WriteFile(filepath.Join(outDir, "csca.der"), cscaDER, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, "dsc.der"), dscDER, 0o644); err != nil {
			return err
		}
	}
	// The canonical public key (x‖y) the commitment is taken over, so tests can
	// feed x/pki's VerifyDsc result without re-parsing the certificate.
	if err := os.WriteFile(filepath.Join(outDir, "dsc_pubkey"), canonical, 0o644); err != nil {
		return err
	}

	// --- Prover.toml ---------------------------------------------------------

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by tools/poafixtures. Do not edit.\n")
	writeByteArray(&b, "dg1", dg1[:])
	fmt.Fprintf(&b, "dg1_len = \"%d\"\n", dg1Len)
	writeByteArray(&b, "e_content", eContent[:])
	fmt.Fprintf(&b, "e_content_len = \"%d\"\n", eContentLen)
	fmt.Fprintf(&b, "dg1_hash_offset = \"%d\"\n", dg1HashOffset)
	writeByteArray(&b, "signed_attrs", signedAttrs[:])
	fmt.Fprintf(&b, "signed_attrs_len = \"%d\"\n", signedAttrsLen)
	fmt.Fprintf(&b, "econtent_hash_offset = \"%d\"\n", eContentHashOffset)
	for _, k := range sortedKeys(keyInputs) {
		fmt.Fprintf(&b, "%s = [%s]\n", k, strings.Join(keyInputs[k], ", "))
	}
	fmt.Fprintf(&b, "current_date = \"%d\"\n", currentDate)

	if err := os.WriteFile(filepath.Join(outDir, "Prover.toml"), []byte(b.String()), 0o644); err != nil {
		return err
	}

	// Each run uses a fresh key (Go's ECDSA cannot be made reproducible), so the
	// values the circuit will output are written next to the fixture rather than
	// left for the caller to recompute.
	null := nullifier(dg1[:])
	if err := os.WriteFile(filepath.Join(outDir, "expected_nullifier"), []byte(null.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "expected_dsc_key"), []byte(dscKey.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("dsc_key   = %s\n", dscKey.String())
	fmt.Printf("nullifier = %s\n", null.String())
	fmt.Printf("msg_hash  = %s\n", hex.EncodeToString(msgHash[:]))
	fmt.Printf("wrote %s fixture inputs to %s\n", v.name, outDir)
	return nil
}

// buildDG1 assembles a DG1 record: a 5-byte tag/length header followed by an
// 88-character TD3 MRZ. The circuit reads the name at offset 10, the date of
// birth at 62 and the expiry at 70, so those must land where TD3 puts them.
func buildDG1() [dg1Max]byte {
	// Padded and length-checked rather than hand-counted: a single character out
	// shifts every field, and the circuit then reads the expiry from the middle
	// of the sex/expiry boundary and fails an opaque range constraint.
	line1 := pad("P<UTOTESTHOLDER<<ALEX", 44)
	// TD3 line 2: passport no (0-8), check (9), nationality (10-12),
	// DOB (13-18), check (19), sex (20), expiry (21-26), ...
	line2 := pad("L898902C36UTO7408122F3002051ZE184226B", 44)
	mrz := line1 + line2
	if len(mrz) != 88 {
		panic(fmt.Sprintf("MRZ must be 88 chars, got %d", len(mrz)))
	}
	if !isDigits(line2[21:27]) || !isDigits(line2[13:19]) {
		panic("MRZ expiry/DOB are not 6 ASCII digits")
	}

	var dg1 [dg1Max]byte
	// 61 5B 5F1F 58: DG1 template, then the MRZ data object of length 88.
	dg1[0], dg1[1], dg1[2], dg1[3], dg1[4] = 0x61, 0x5B, 0x5F, 0x1F, 0x58
	copy(dg1[5:], mrz)
	return dg1
}

// embed places a 32-byte hash at `offset` inside a fixed-size buffer of
// otherwise arbitrary bytes, returning the buffer, its logical length and the
// offset — mirroring how a real SOD carries the hash inside DER structure.
func embed(hash []byte, offset int) ([signedAttrsMax]byte, int, int) {
	var buf [signedAttrsMax]byte
	for i := range buf {
		buf[i] = byte(i * 7) // deterministic filler
	}
	copy(buf[offset:], hash)
	return buf, offset + 32 + 8, offset
}

// commitment is the circuit's dsc_commitment: Poseidon2 over the key bytes, one
// field element per byte.
func commitment(pubkey []byte) fr.Element {
	elems := make([]fr.Element, len(pubkey))
	for i, b := range pubkey {
		elems[i].SetUint64(uint64(b))
	}
	return poseidon2.Hash(elems)
}

// nullifier mirrors poa_core::finalize: Poseidon2 over the 39 name bytes
// followed by the 6 date-of-birth bytes.
func nullifier(dg1 []byte) fr.Element {
	const nameOffset, nameLen, dobOffset = 10, 39, 62
	elems := make([]fr.Element, 45)
	for i := 0; i < nameLen; i++ {
		elems[i].SetUint64(uint64(dg1[nameOffset+i]))
	}
	for i := 0; i < 6; i++ {
		elems[nameLen+i].SetUint64(uint64(dg1[dobOffset+i]))
	}
	return poseidon2.Hash(elems)
}

// makeChain issues a self-signed CSCA and a DSC carrying the Document Signer's
// public key, so the chain's x/pki verifies the same signer the circuit proved
// with. The CSCA is always P-256 — only the DSC's key type has to vary, since
// that is what the register circuit constrains.
func makeChain(_ any, dscPub any) (cscaDER, dscDER []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	notBefore := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)

	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CSCA", Country: []string{"UT"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		SubjectKeyId:          []byte{1, 2, 3, 4, 5},
	}
	cscaDER, err = x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(cscaDER)
	if err != nil {
		return nil, nil, err
	}

	dscTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test DSC", Country: []string{"UT"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	dscDER, err = x509.CreateCertificate(rand.Reader, dscTmpl, caCert, dscPub, caKey)
	if err != nil {
		return nil, nil, err
	}
	return cscaDER, dscDER, nil
}

// pad right-fills an MRZ field with the filler character to a fixed width.
func pad(s string, width int) string {
	if len(s) > width {
		panic(fmt.Sprintf("MRZ line %q exceeds %d chars", s, width))
	}
	return s + strings.Repeat("<", width-len(s))
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func writeByteArray(b *strings.Builder, name string, data []byte) {
	parts := make([]string, len(data))
	for i, v := range data {
		parts[i] = fmt.Sprintf("\"%d\"", v)
	}
	fmt.Fprintf(b, "%s = [%s]\n", name, strings.Join(parts, ", "))
}
