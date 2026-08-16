// Package certs does the native (non-ZK) passport-PKI certificate work for the
// DSC registry: verifying that a Document Signer Certificate (DSC) was signed by
// a trusted CSCA, and deriving the canonical public-key bytes that become the
// registry Merkle leaf. This is the "DSC->CSCA verified once, on-chain, in Go"
// half of Option C (see docs/DSC_REGISTRY_OPTION_C.md).
package certs

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// KeyType identifies a DSC/CSCA public-key algorithm (selects the leaf encoding
// and, for the user proof, the SOD->DSC circuit variant).
type KeyType string

const (
	KeyECDSA KeyType = "ecdsa"
	KeyRSA   KeyType = "rsa"
)

// DSC is a parsed, verified Document Signer Certificate ready for the registry.
type DSC struct {
	KeyType   KeyType
	Pubkey    []byte // canonical encoding hashed into the registry leaf
	NotAfter  time.Time
	CurveBits int // ECDSA only
	KeyBits   int // RSA only
}

// VerifyDSC parses a DSC certificate (DER), verifies its signature was produced
// by csca, checks it is valid at `now`, and returns its canonical public key.
//
// It uses the raw CheckSignature (not full X.509 chain verification) on purpose:
// ICAO certificates routinely violate strict X.509 policy (NULL algorithm params,
// non-positive serials, SKI/AKI quirks), so callers select the issuing CSCA
// out-of-band (by AKI/subject) and this only checks the cryptographic link.
func VerifyDSC(dscDER []byte, csca *x509.Certificate, now time.Time) (*DSC, error) {
	dsc, err := x509.ParseCertificate(dscDER)
	if err != nil {
		return nil, fmt.Errorf("parse DSC: %w", err)
	}
	if now.Before(dsc.NotBefore) {
		return nil, fmt.Errorf("DSC not yet valid (NotBefore %s)", dsc.NotBefore)
	}
	if now.After(dsc.NotAfter) {
		return nil, fmt.Errorf("DSC expired (NotAfter %s)", dsc.NotAfter)
	}
	// The DSC->CSCA cryptographic link: csca's key signed the DSC's TBS.
	if err := csca.CheckSignature(dsc.SignatureAlgorithm, dsc.RawTBSCertificate, dsc.Signature); err != nil {
		return nil, fmt.Errorf("DSC not signed by the given CSCA: %w", err)
	}
	return canonicalPubkey(dsc)
}

// canonicalPubkey returns the registry-leaf encoding of a certificate's public
// key: ECDSA -> x‖y (each coordinate padded to the curve byte length); RSA ->
// the modulus in big-endian.
func canonicalPubkey(cert *x509.Certificate) (*DSC, error) {
	switch pub := cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		coordLen := (pub.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*coordLen)
		pub.X.FillBytes(out[:coordLen])
		pub.Y.FillBytes(out[coordLen:])
		return &DSC{
			KeyType:   KeyECDSA,
			Pubkey:    out,
			NotAfter:  cert.NotAfter,
			CurveBits: pub.Curve.Params().BitSize,
		}, nil
	case *rsa.PublicKey:
		return &DSC{
			KeyType:  KeyRSA,
			Pubkey:   pub.N.Bytes(),
			NotAfter: cert.NotAfter,
			KeyBits:  pub.N.BitLen(),
		}, nil
	default:
		return nil, errors.New("unsupported DSC public-key algorithm")
	}
}
