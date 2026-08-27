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
