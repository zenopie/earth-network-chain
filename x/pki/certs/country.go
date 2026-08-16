package certs

import (
	"encoding/asn1"
	"strings"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// countryOID is X.520 id-at-countryName (2.5.4.6); its value is an ISO 3166-1
// alpha-2 code.
var countryOID = asn1.ObjectIdentifier{2, 5, 4, 6}

// Country returns the issuing country of the certificate as an uppercase ISO
// 3166-1 alpha-2 code, or "" if the name carries no country attribute.
//
// For a Document Signer this is the state that issued the passport, which is
// what makes per-country registration statistics possible at all. The subject is
// preferred and the issuer used as a fallback: a few national DSCs omit the
// country from their own subject while their CSCA carries it.
func (c *Cert) Country() string {
	if cc := countryFromName(c.SubjectRaw); cc != "" {
		return cc
	}
	return countryFromName(c.IssuerRaw)
}

// countryFromName walks a DER RDNSequence looking for the countryName attribute.
//
// Parsed leniently and by hand, for the same reason the rest of this package is:
// real passport certificates include name encodings crypto/x509 rejects outright,
// and a malformed name must degrade to "unknown country" rather than fail a
// registration.
func countryFromName(der []byte) string {
	outer := cryptobyte.String(der)
	var rdnSeq cryptobyte.String
	if !outer.ReadASN1(&rdnSeq, cbasn1.SEQUENCE) {
		// Some encoders hand us the sequence contents without the wrapper.
		rdnSeq = cryptobyte.String(der)
	}

	for !rdnSeq.Empty() {
		var rdn cryptobyte.String
		if !rdnSeq.ReadASN1(&rdn, cbasn1.SET) {
			return ""
		}
		for !rdn.Empty() {
			var attr cryptobyte.String
			if !rdn.ReadASN1(&attr, cbasn1.SEQUENCE) {
				return ""
			}
			var oid asn1.ObjectIdentifier
			if !attr.ReadASN1ObjectIdentifier(&oid) {
				return ""
			}
			// The value is a DirectoryString: PrintableString in practice, but
			// UTF8String and others occur, so accept any primitive string tag.
			var value cryptobyte.String
			var tag cbasn1.Tag
			if !attr.ReadAnyASN1(&value, &tag) {
				return ""
			}
			if oid.Equal(countryOID) {
				cc := strings.ToUpper(strings.TrimSpace(string(value)))
				if len(cc) != 2 {
					return "" // not an alpha-2 code; treat as unknown
				}
				return cc
			}
		}
	}
	return ""
}
