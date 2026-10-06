package certs

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/asn1"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// explicitParams encodes ECParameters (ICAO 9303-12 4.1.6.3) for c, with b
// replaced by bOverride when non-nil.
func explicitParams(c *Curve, bOverride *big.Int) []byte {
	w := c.byteLen
	pad := func(x *big.Int) []byte { out := make([]byte, w); x.FillBytes(out); return out }
	b := c.B
	if bOverride != nil {
		b = bOverride
	}
	var bld cryptobyte.Builder
	bld.AddASN1(cbasn1.SEQUENCE, func(s *cryptobyte.Builder) {
		s.AddASN1Int64(1)
		s.AddASN1(cbasn1.SEQUENCE, func(f *cryptobyte.Builder) {
			f.AddASN1ObjectIdentifier([]int{1, 2, 840, 10045, 1, 1})
			f.AddASN1BigInt(c.P)
		})
		s.AddASN1(cbasn1.SEQUENCE, func(cv *cryptobyte.Builder) {
			cv.AddASN1OctetString(pad(c.A))
			cv.AddASN1OctetString(pad(b))
		})
		s.AddASN1OctetString(append(append([]byte{4}, pad(c.Gx)...), pad(c.Gy)...))
		s.AddASN1BigInt(c.N)
		s.AddASN1Int64(1)
	})
	return bld.BytesOrPanic()
}

// TestExplicitParametersMatchEveryParameter: explicit domain parameters name a
// supported curve (and so its commitment tag) only when all of them match.
func TestExplicitParametersMatchEveryParameter(t *testing.T) {
	for _, c := range []*Curve{nistP224(), brainpoolP224r1(), brainpoolP256r1(), brainpoolP512r1(), nistP521()} {
		got, err := parseExplicitECParams(cryptobyte.String(explicitParams(c, nil)))
		if err != nil || got.Name != c.Name {
			t.Fatalf("%s: got %v, %v", c.Name, got, err)
		}
		if _, err := CurveTagByName(got.Name); err != nil {
			t.Fatalf("%s has no tag: %v", c.Name, err)
		}
		// Same prime, different b: not that curve, and no tag.
		other, err := parseExplicitECParams(cryptobyte.String(explicitParams(c, new(big.Int).Add(c.B, big.NewInt(1)))))
		if err != nil {
			t.Fatal(err)
		}
		if other.Name == c.Name {
			t.Fatalf("%s: a different b was taken for %s", c.Name, c.Name)
		}
		if _, err := CurveTagByName(other.Name); err == nil {
			t.Fatalf("%s: a curve outside the table got a commitment tag", c.Name)
		}
	}
}

func pssParams(hash, mgf []int, salt, trailer int64) []byte {
	alg := func(b *cryptobyte.Builder, oid []int) {
		b.AddASN1(cbasn1.SEQUENCE, func(a *cryptobyte.Builder) {
			a.AddASN1ObjectIdentifier(oid)
			a.AddASN1NULL()
		})
	}
	var bld cryptobyte.Builder
	bld.AddASN1(cbasn1.SEQUENCE, func(s *cryptobyte.Builder) {
		s.AddASN1(cbasn1.Tag(0).Constructed().ContextSpecific(), func(f *cryptobyte.Builder) { alg(f, hash) })
		s.AddASN1(cbasn1.Tag(1).Constructed().ContextSpecific(), func(f *cryptobyte.Builder) {
			f.AddASN1(cbasn1.SEQUENCE, func(m *cryptobyte.Builder) {
				m.AddASN1ObjectIdentifier([]int{1, 2, 840, 113549, 1, 1, 8})
				alg(m, mgf)
			})
		})
		s.AddASN1(cbasn1.Tag(2).Constructed().ContextSpecific(), func(f *cryptobyte.Builder) { f.AddASN1Int64(salt) })
		s.AddASN1(cbasn1.Tag(3).Constructed().ContextSpecific(), func(f *cryptobyte.Builder) { f.AddASN1Int64(trailer) })
	})
	return bld.BytesOrPanic()
}

// TestPSSParameters: crypto/rsa.VerifyPSS assumes MGF1 over the message hash
// and trailer 1, so a certificate stating anything else is refused rather than
// verified under parameters it does not have.
func TestPSSParameters(t *testing.T) {
	sha256OID := []int{2, 16, 840, 1, 101, 3, 4, 2, 1}
	sha1OID := []int{1, 3, 14, 3, 2, 26}
	if h, err := pssHash(pssParams(sha256OID, sha256OID, 32, 1)); err != nil || h != crypto.SHA256 {
		t.Fatalf("SHA-256 PSS: %v, %v", h, err)
	}
	if _, err := pssHash(pssParams(sha256OID, sha1OID, 32, 1)); err == nil {
		t.Fatal("MGF1-SHA-1 under SHA-256 was accepted")
	}
	if _, err := pssHash(pssParams(sha256OID, sha256OID, 32, 2)); err == nil {
		t.Fatal("trailer field 2 was accepted")
	}
	if h, err := pssHash(nil); err != nil || h != crypto.SHA1 {
		t.Fatalf("absent params: %v, %v", h, err)
	}
}

// TestSHA224WithRSA: a DSC signed sha224WithRSAEncryption verifies through
// VerifySignedBy (the path registration takes), and a flipped bit does not.
func TestSHA224WithRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tbs := []byte("a DSC TBSCertificate")
	d := sha256.Sum224(tbs)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA224, d[:])
	if err != nil {
		t.Fatal(err)
	}
	cert := &Cert{RawTBS: tbs, SigAlgo: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 14}, Signature: sig}
	signer := &PublicKey{IsRSA: true, RSAModulus: key.N, RSAExp: key.E}
	if err := VerifySignedBy(cert, signer); err != nil {
		t.Fatalf("sha224WithRSAEncryption: %v", err)
	}
	cert.RawTBS = []byte("a DSC TBSCertificatf")
	if VerifySignedBy(cert, signer) == nil {
		t.Fatal("a changed TBS verified")
	}
}

// TestSigAlgoMustMatchKeyType: an ECDSA OID over an RSA signer (and the
// reverse) is refused rather than verified under the other scheme.
func TestSigAlgoMustMatchKeyType(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &Cert{RawTBS: []byte("x"), SigAlgo: asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}, Signature: []byte{0}}
	signer := &PublicKey{IsRSA: true, RSAModulus: key.N, RSAExp: key.E}
	if err := VerifySignedBy(cert, signer); err == nil || !strings.Contains(err.Error(), "key type") {
		t.Fatalf("ECDSA OID under an RSA key: %v", err)
	}
}
