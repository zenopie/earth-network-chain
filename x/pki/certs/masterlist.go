package certs

import (
	"encoding/asn1"
	"errors"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// ParseMasterList extracts the DER of every CSCA certificate from an ICAO CSCA
// Master List (`.ml`) — a CMS SignedData whose eContent is a CscaMasterList:
//
//	ContentInfo -> SignedData -> encapContentInfo.eContent (OCTET STRING) ->
//	CscaMasterList ::= SEQUENCE { version INTEGER, certList SET OF Certificate }
//
// It does not verify the master list's own signature; callers seed the returned
// certificates as trusted anchors via governance/genesis.
func ParseMasterList(der []byte) ([][]byte, error) {
	input := cryptobyte.String(der)
	var ci cryptobyte.String
	if !input.ReadASN1(&ci, cbasn1.SEQUENCE) {
		return nil, errors.New("masterlist: bad ContentInfo")
	}
	var oid asn1.ObjectIdentifier
	if !ci.ReadASN1ObjectIdentifier(&oid) {
		return nil, errors.New("masterlist: bad contentType")
	}
	// content [0] EXPLICIT SignedData
	var content cryptobyte.String
	if !ci.ReadASN1(&content, cbasn1.Tag(0).Constructed().ContextSpecific()) {
		return nil, errors.New("masterlist: bad content [0]")
	}
	var sd cryptobyte.String
	if !content.ReadASN1(&sd, cbasn1.SEQUENCE) {
		return nil, errors.New("masterlist: bad SignedData")
	}
	if !sd.SkipASN1(cbasn1.INTEGER) || !sd.SkipASN1(cbasn1.SET) { // version, digestAlgorithms
		return nil, errors.New("masterlist: bad SignedData header")
	}
	var eci cryptobyte.String
	if !sd.ReadASN1(&eci, cbasn1.SEQUENCE) { // encapContentInfo
		return nil, errors.New("masterlist: bad encapContentInfo")
	}
	if !eci.SkipASN1(cbasn1.OBJECT_IDENTIFIER) { // eContentType
		return nil, errors.New("masterlist: bad eContentType")
	}
	// eContent [0] EXPLICIT OCTET STRING
	var eWrap cryptobyte.String
	if !eci.ReadASN1(&eWrap, cbasn1.Tag(0).Constructed().ContextSpecific()) {
		return nil, errors.New("masterlist: bad eContent [0]")
	}
	var eContent cryptobyte.String
	if !eWrap.ReadASN1(&eContent, cbasn1.OCTET_STRING) {
		return nil, errors.New("masterlist: bad eContent OCTET STRING")
	}
	// CscaMasterList SEQUENCE { version, SET OF Certificate }
	var cml cryptobyte.String
	if !eContent.ReadASN1(&cml, cbasn1.SEQUENCE) {
		return nil, errors.New("masterlist: bad CscaMasterList")
	}
	if !cml.SkipASN1(cbasn1.INTEGER) { // version
		return nil, errors.New("masterlist: bad CscaMasterList version")
	}
	var certSet cryptobyte.String
	if !cml.ReadASN1(&certSet, cbasn1.SET) {
		return nil, errors.New("masterlist: bad certList SET")
	}
	var out [][]byte
	for !certSet.Empty() {
		var cert cryptobyte.String
		if !certSet.ReadASN1Element(&cert, cbasn1.SEQUENCE) {
			return nil, errors.New("masterlist: bad certificate in list")
		}
		out = append(out, append([]byte(nil), cert...))
	}
	return out, nil
}
