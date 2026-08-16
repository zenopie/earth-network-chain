package certs

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// PublicKey is a parsed certificate public key (RSA or EC on any short-Weierstrass
// curve, including Brainpool / explicit-parameter curves the stdlib rejects).
type PublicKey struct {
	IsRSA      bool
	RSAModulus *big.Int
	RSAExp     int
	Curve      *Curve
	X, Y       *big.Int
	Raw        []byte // the SubjectPublicKey BIT STRING contents (for the leaf)
}

// Cert is a leniently-parsed certificate: enough to verify its signature and read
// its key, without stdlib's policy restrictions.
type Cert struct {
	RawTBS     []byte
	SigAlgo    asn1.ObjectIdentifier
	SigParams  []byte // signatureAlgorithm parameters (RSASSA-PSS-params)
	Signature  []byte
	IssuerRaw  []byte
	SubjectRaw []byte
	NotBefore  time.Time
	NotAfter   time.Time
	PublicKey  *PublicKey
	SKI        []byte // subjectKeyIdentifier (2.5.29.14)
	AKI        []byte // authorityKeyIdentifier keyIdentifier (2.5.29.35)
}

const (
	oidEcPublicKey = "1.2.840.10045.2.1"
	oidRSA         = "1.2.840.113549.1.1.1"
	oidPrimeField  = "1.2.840.10045.1.1"
)

// ParseCert leniently parses an X.509 certificate DER.
func ParseCert(der []byte) (*Cert, error) {
	input := cryptobyte.String(der)
	var cert cryptobyte.String
	if !input.ReadASN1(&cert, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad outer SEQUENCE")
	}
	var tbs cryptobyte.String
	if !cert.ReadASN1Element(&tbs, cbasn1.SEQUENCE) { // keep tag+len: this is what's signed
		return nil, errors.New("x509: bad tbsCertificate")
	}
	rawTBS := []byte(tbs)

	var sigAlgo cryptobyte.String
	if !cert.ReadASN1(&sigAlgo, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad signatureAlgorithm")
	}
	var sigOID asn1.ObjectIdentifier
	if !sigAlgo.ReadASN1ObjectIdentifier(&sigOID) {
		return nil, errors.New("x509: bad signatureAlgorithm OID")
	}
	sigParams := append([]byte(nil), sigAlgo...) // remaining bytes = algorithm params (RSA-PSS)
	var sigBits asn1.BitString
	if !cert.ReadASN1BitString(&sigBits) {
		return nil, errors.New("x509: bad signatureValue")
	}

	// --- inside tbsCertificate ---
	var body cryptobyte.String
	tbsStr := cryptobyte.String(rawTBS)
	if !tbsStr.ReadASN1(&body, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad TBS body")
	}
	body.SkipOptionalASN1(cbasn1.Tag(0).Constructed().ContextSpecific()) // version [0]
	if !body.SkipASN1(cbasn1.INTEGER) {                                  // serialNumber (may be negative)
		return nil, errors.New("x509: bad serial")
	}
	if !body.SkipASN1(cbasn1.SEQUENCE) { // inner signature algorithm
		return nil, errors.New("x509: bad inner sigAlgo")
	}
	var issuer, subject cryptobyte.String
	if !body.ReadASN1Element(&issuer, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad issuer")
	}
	notBefore, notAfter, err := parseValidity(&body)
	if err != nil {
		return nil, err
	}
	if !body.ReadASN1Element(&subject, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad subject")
	}
	pub, err := parseSPKI(&body)
	if err != nil {
		return nil, err
	}

	c := &Cert{
		RawTBS:     rawTBS,
		SigAlgo:    sigOID,
		SigParams:  sigParams,
		Signature:  sigBits.RightAlign(),
		IssuerRaw:  []byte(issuer),
		SubjectRaw: []byte(subject),
		NotBefore:  notBefore,
		NotAfter:   notAfter,
		PublicKey:  pub,
	}
	parseExtensions(&body, c) // best-effort: SKI/AKI for issuer matching
	return c, nil
}

// parseExtensions reads the optional uniqueIDs + extensions [3] and pulls out the
// subject/authority key identifiers used to match a DSC/link cert to its issuer.
func parseExtensions(body *cryptobyte.String, c *Cert) {
	body.SkipOptionalASN1(cbasn1.Tag(1).ContextSpecific()) // issuerUniqueID [1]
	body.SkipOptionalASN1(cbasn1.Tag(2).ContextSpecific()) // subjectUniqueID [2]
	var wrap cryptobyte.String
	var present bool
	if !body.ReadOptionalASN1(&wrap, &present, cbasn1.Tag(3).Constructed().ContextSpecific()) || !present {
		return
	}
	var exts cryptobyte.String
	if !wrap.ReadASN1(&exts, cbasn1.SEQUENCE) {
		return
	}
	for !exts.Empty() {
		var ext cryptobyte.String
		if !exts.ReadASN1(&ext, cbasn1.SEQUENCE) {
			return
		}
		var oid asn1.ObjectIdentifier
		if !ext.ReadASN1ObjectIdentifier(&oid) {
			continue
		}
		ext.SkipOptionalASN1(cbasn1.BOOLEAN) // critical DEFAULT false
		var val cryptobyte.String
		if !ext.ReadASN1(&val, cbasn1.OCTET_STRING) {
			continue
		}
		switch oid.String() {
		case "2.5.29.14": // subjectKeyIdentifier: OCTET STRING wrapping the key id
			var ski cryptobyte.String
			if val.ReadASN1(&ski, cbasn1.OCTET_STRING) {
				c.SKI = append([]byte(nil), ski...)
			}
		case "2.5.29.35": // authorityKeyIdentifier: SEQUENCE { [0] keyIdentifier ... }
			var seq cryptobyte.String
			if !val.ReadASN1(&seq, cbasn1.SEQUENCE) {
				continue
			}
			var keyid cryptobyte.String
			var p bool
			if seq.ReadOptionalASN1(&keyid, &p, cbasn1.Tag(0).ContextSpecific()) && p {
				c.AKI = append([]byte(nil), keyid...)
			}
		}
	}
}

// parseValidity reads the Validity SEQUENCE { notBefore, notAfter } where each
// time is a UTCTime or GeneralizedTime.
func parseValidity(body *cryptobyte.String) (time.Time, time.Time, error) {
	var v cryptobyte.String
	if !body.ReadASN1(&v, cbasn1.SEQUENCE) {
		return time.Time{}, time.Time{}, errors.New("x509: bad validity")
	}
	nb, err := readTime(&v)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	na, err := readTime(&v)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return nb, na, nil
}

func readTime(s *cryptobyte.String) (time.Time, error) {
	var raw cryptobyte.String
	var tag cbasn1.Tag
	if !s.ReadAnyASN1(&raw, &tag) {
		return time.Time{}, errors.New("x509: bad time")
	}
	str := string(raw)
	switch tag {
	case cbasn1.UTCTime:
		t, err := time.Parse("060102150405Z0700", str)
		if err != nil {
			t, err = time.Parse("0601021504Z0700", str) // some omit seconds
		}
		return t, err
	case cbasn1.GeneralizedTime:
		return time.Parse("20060102150405Z0700", str)
	default:
		return time.Time{}, fmt.Errorf("x509: unexpected time tag %v", tag)
	}
}

func parseSPKI(body *cryptobyte.String) (*PublicKey, error) {
	var spki cryptobyte.String
	if !body.ReadASN1(&spki, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad SPKI")
	}
	var algo cryptobyte.String
	if !spki.ReadASN1(&algo, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad SPKI algorithm")
	}
	var algOID asn1.ObjectIdentifier
	if !algo.ReadASN1ObjectIdentifier(&algOID) {
		return nil, errors.New("x509: bad SPKI algorithm OID")
	}
	var keyBits asn1.BitString
	if !spki.ReadASN1BitString(&keyBits) {
		return nil, errors.New("x509: bad SPKI key")
	}
	keyBytes := keyBits.RightAlign()

	switch algOID.String() {
	case oidRSA:
		n, e, err := parseRSAKey(keyBytes)
		if err != nil {
			return nil, err
		}
		return &PublicKey{IsRSA: true, RSAModulus: n, RSAExp: e, Raw: keyBytes}, nil
	case oidEcPublicKey:
		curve, err := resolveCurve(algo) // remaining bytes of `algo` = EC params
		if err != nil {
			return nil, err
		}
		x, y, err := uncompressedPoint(keyBytes, curve)
		if err != nil {
			return nil, err
		}
		return &PublicKey{Curve: curve, X: x, Y: y, Raw: keyBytes}, nil
	default:
		return nil, fmt.Errorf("x509: unsupported public-key algorithm %s", algOID)
	}
}

func parseRSAKey(der []byte) (*big.Int, int, error) {
	var pub struct {
		N *big.Int
		E int
	}
	if _, err := asn1.Unmarshal(der, &pub); err != nil {
		return nil, 0, fmt.Errorf("x509: bad RSA key: %w", err)
	}
	return pub.N, pub.E, nil
}

func uncompressedPoint(b []byte, c *Curve) (*big.Int, *big.Int, error) {
	if len(b) != 1+2*c.byteLen || b[0] != 0x04 {
		return nil, nil, errors.New("x509: EC point not uncompressed / wrong length")
	}
	x := new(big.Int).SetBytes(b[1 : 1+c.byteLen])
	y := new(big.Int).SetBytes(b[1+c.byteLen:])
	return x, y, nil
}

// resolveCurve reads an EC domain parameter (named OID or explicit ECParameters).
func resolveCurve(params cryptobyte.String) (*Curve, error) {
	// Named curve: an OID.
	probe := params
	var oid asn1.ObjectIdentifier
	if probe.ReadASN1ObjectIdentifier(&oid) {
		if c := namedCurves[oid.String()]; c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("x509: unsupported named curve %s", oid)
	}
	// Explicit ECParameters SEQUENCE.
	return parseExplicitECParams(params)
}

func parseExplicitECParams(params cryptobyte.String) (*Curve, error) {
	var ec cryptobyte.String
	if !params.ReadASN1(&ec, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad explicit ECParameters")
	}
	if !ec.SkipASN1(cbasn1.INTEGER) { // version (ecdpVer1)
		return nil, errors.New("x509: bad ECParameters version")
	}
	// fieldID SEQUENCE { fieldType OID, prime INTEGER }
	var fieldID cryptobyte.String
	if !ec.ReadASN1(&fieldID, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad fieldID")
	}
	var fieldType asn1.ObjectIdentifier
	fieldID.ReadASN1ObjectIdentifier(&fieldType)
	if fieldType.String() != oidPrimeField {
		return nil, fmt.Errorf("x509: unsupported field type %s", fieldType)
	}
	p := new(big.Int)
	if !fieldID.ReadASN1Integer(p) {
		return nil, errors.New("x509: bad field prime")
	}
	// curve SEQUENCE { a OCTET STRING, b OCTET STRING, seed BIT STRING OPTIONAL }
	var curveSeq cryptobyte.String
	if !ec.ReadASN1(&curveSeq, cbasn1.SEQUENCE) {
		return nil, errors.New("x509: bad curve")
	}
	var aBytes, bBytes cryptobyte.String
	if !curveSeq.ReadASN1(&aBytes, cbasn1.OCTET_STRING) || !curveSeq.ReadASN1(&bBytes, cbasn1.OCTET_STRING) {
		return nil, errors.New("x509: bad curve a/b")
	}
	a := new(big.Int).SetBytes(aBytes)
	bb := new(big.Int).SetBytes(bBytes)
	// base OCTET STRING (0x04 || Gx || Gy)
	var base cryptobyte.String
	if !ec.ReadASN1(&base, cbasn1.OCTET_STRING) {
		return nil, errors.New("x509: bad base point")
	}
	byteLen := (p.BitLen() + 7) / 8
	if len(base) != 1+2*byteLen || base[0] != 0x04 {
		return nil, errors.New("x509: bad base point encoding")
	}
	gx := new(big.Int).SetBytes(base[1 : 1+byteLen])
	gy := new(big.Int).SetBytes(base[1+byteLen:])
	// order INTEGER
	n := new(big.Int)
	if !ec.ReadASN1Integer(n) {
		return nil, errors.New("x509: bad order")
	}
	// Normalise to a known curve when the prime matches (keeps a canonical name).
	if known := byPrime[p.String()]; known != nil && known.N.Cmp(n) == 0 {
		return known, nil
	}
	return &Curve{Name: "explicit", P: p, A: a, B: bb, Gx: gx, Gy: gy, N: n, byteLen: byteLen}, nil
}

const oidRSAPSS = "1.2.840.113549.1.1.10"

// VerifySignedBy verifies that `cert`'s signature was produced by `signer`.
func VerifySignedBy(cert *Cert, signer *PublicKey) error {
	// RSA-PSS carries its hash in the algorithm parameters.
	if cert.SigAlgo.String() == oidRSAPSS {
		if !signer.IsRSA {
			return errors.New("x509: RSA-PSS signature but signer is not RSA")
		}
		ch, err := pssHash(cert.SigParams)
		if err != nil {
			return err
		}
		hh := ch.New()
		hh.Write(cert.RawTBS)
		pub := &rsa.PublicKey{N: signer.RSAModulus, E: signer.RSAExp}
		return rsa.VerifyPSS(pub, ch, hh.Sum(nil), cert.Signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto, Hash: ch})
	}

	h, ok := hashFor(cert.SigAlgo)
	if !ok {
		return fmt.Errorf("x509: unsupported signature algorithm %s", cert.SigAlgo)
	}
	h.Write(cert.RawTBS)
	digest := h.Sum(nil)

	if signer.IsRSA {
		ch, ok := cryptoHashFor(cert.SigAlgo)
		if !ok {
			return fmt.Errorf("x509: unsupported RSA hash %s", cert.SigAlgo)
		}
		pub := &rsa.PublicKey{N: signer.RSAModulus, E: signer.RSAExp}
		return rsa.VerifyPKCS1v15(pub, ch, digest, cert.Signature)
	}
	r, s, err := parseECDSASig(cert.Signature)
	if err != nil {
		return err
	}
	if !signer.Curve.verifyECDSA(signer.X, signer.Y, r, s, digest) {
		return errors.New("x509: ECDSA signature verification failed")
	}
	return nil
}

// IsSelfIssued reports whether issuer == subject (a self-signed root candidate).
func (c *Cert) IsSelfIssued() bool { return bytes.Equal(c.IssuerRaw, c.SubjectRaw) }

// KeyType returns the DSC/CSCA public-key algorithm (selects the registry leaf
// encoding and, for the user proof, the SOD->DSC circuit variant).
func (pk *PublicKey) KeyType() KeyType {
	if pk.IsRSA {
		return KeyRSA
	}
	return KeyECDSA
}

// CanonicalBytes returns the bytes hashed into the registry Merkle leaf, matching
// the circuit: ECDSA -> x‖y (each coordinate padded to the curve byte length),
// RSA -> the modulus in big-endian.
func (pk *PublicKey) CanonicalBytes() []byte {
	if pk.IsRSA {
		return pk.RSAModulus.Bytes()
	}
	out := make([]byte, 2*pk.Curve.byteLen)
	pk.X.FillBytes(out[:pk.Curve.byteLen])
	pk.Y.FillBytes(out[pk.Curve.byteLen:])
	return out
}

// pssHash reads the hash algorithm from RSASSA-PSS-params (default SHA-1).
func pssHash(params []byte) (crypto.Hash, error) {
	if len(params) == 0 {
		return crypto.SHA1, nil
	}
	s := cryptobyte.String(params)
	var seq cryptobyte.String
	if !s.ReadASN1(&seq, cbasn1.SEQUENCE) {
		return 0, errors.New("x509: bad RSASSA-PSS-params")
	}
	var ha cryptobyte.String
	var present bool
	if !seq.ReadOptionalASN1(&ha, &present, cbasn1.Tag(0).Constructed().ContextSpecific()) || !present {
		return crypto.SHA1, nil // hashAlgorithm DEFAULT sha1
	}
	var alg cryptobyte.String
	if !ha.ReadASN1(&alg, cbasn1.SEQUENCE) {
		return 0, errors.New("x509: bad PSS hashAlgorithm")
	}
	var oid asn1.ObjectIdentifier
	if !alg.ReadASN1ObjectIdentifier(&oid) {
		return 0, errors.New("x509: bad PSS hash OID")
	}
	switch oid.String() {
	case "1.3.14.3.2.26":
		return crypto.SHA1, nil
	case "2.16.840.1.101.3.4.2.1":
		return crypto.SHA256, nil
	case "2.16.840.1.101.3.4.2.2":
		return crypto.SHA384, nil
	case "2.16.840.1.101.3.4.2.3":
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("x509: unsupported PSS hash %s", oid)
}

func cryptoHashFor(oid asn1.ObjectIdentifier) (crypto.Hash, bool) {
	switch oid.String() {
	case "1.2.840.113549.1.1.5":
		return crypto.SHA1, true
	case "1.2.840.113549.1.1.11":
		return crypto.SHA256, true
	case "1.2.840.113549.1.1.12":
		return crypto.SHA384, true
	case "1.2.840.113549.1.1.13":
		return crypto.SHA512, true
	}
	return 0, false
}
