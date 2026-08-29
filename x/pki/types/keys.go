package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name.
	ModuleName = "pki"
	// StoreKey defines the primary module store key.
	StoreKey = ModuleName
	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	GovModuleName = "gov"
)

// What one call to VerifyDsc is allowed to cost.
//
// x/personhood charges a single flat DscVerificationGas before calling it, and
// that number only means anything if the work behind it has a ceiling. Two
// inputs the submitter controls would otherwise set it: how large a public key
// the certificate declares, and how many trust-store certificates its issuer
// names. Neither is bounded by the transaction size in any useful way — a small
// DER can declare a very large modulus, and the candidate count is a property
// of the store rather than of the message.
//
// These are the ceilings. Together they make the worst case statable: at most
// MaxIssuerCandidates signature verifications over a key of at most
// MaxPublicKeyBytes. Raise either and DscVerificationGas has to be revisited
// with it.
// The key-size half of that ceiling lives in x/pki/certs as
// certs.MaxPublicKeyBytes, next to the parser that enforces it.
const (
	// MaxIssuerCandidates is how many trust-store certificates one DSC may be
	// checked against before the chain gives up.
	//
	// Candidates come from two lookups — certificates whose SKI matches the
	// DSC's AKI, and certificates whose subject DN equals its issuer DN — and
	// certificates sharing an SKI share a public key, so in practice the first
	// is decisive and the rest are never reached. The list is only long when one
	// country has accumulated many signing identities under one DN.
	//
	// Measured, not guessed: the largest per-DN group in the bundled ICAO master
	// list is 13 certificates, so a cap of 16 would have left three renewals of
	// headroom on a store that grows every year. Thirty-two roughly doubles the
	// worst case that exists today and still bounds the work at a stateable
	// number of signature verifications.
	//
	// The pathological input is a certificate that verifies against none of its
	// candidates, since that is the one that tries them all; a DSC that really
	// needs a thirty-third is a signal the trust store wants pruning, and the
	// error says so rather than failing as the vaguer "nothing verified this".
	//
	// TestTrustStoreFitsTheCaps holds this against the real store.
	MaxIssuerCandidates = 32
)

// ParamsKey is the prefix for module params.
var ParamsKey = collections.NewPrefix("p_pki")

// Storage prefixes.
var (
	CscasKey       = collections.NewPrefix("cscas")        // certID -> Csca
	CscaBySKIKey   = collections.NewPrefix("csca_by_ski")  // (SKI, certID) -> present
	CscaByDNKey    = collections.NewPrefix("csca_by_dn")   // (sha256(subjectDN), certID) -> present
	RevokedDscsKey = collections.NewPrefix("revoked_dscs") // sha256(dsc pubkey) -> present
	// RevokedDscCommitmentsKey is the same set keyed by Poseidon2 commitment.
	RevokedDscCommitmentsKey = collections.NewPrefix("revoked_dsc_commitments") // poseidon2(dsc pubkey) -> present
	// RevokedCscasKey is the set of CSCA signing keys governance has withdrawn
	// trust from. Keyed by the key rather than the certificate because several
	// certificates can carry one key — see MsgRevokeCsca in tx.proto.
	RevokedCscasKey = collections.NewPrefix("revoked_cscas") // sha256(csca pubkey) -> present
)
