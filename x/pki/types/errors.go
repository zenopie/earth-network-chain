package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrInvalidCert  = errorsmod.Register(ModuleName, 2, "invalid certificate")
	ErrNoIssuerCsca = errorsmod.Register(ModuleName, 3, "no trusted issuing CSCA found")
	ErrCertVerify   = errorsmod.Register(ModuleName, 4, "certificate signature verification failed")
	ErrCertExpired  = errorsmod.Register(ModuleName, 5, "certificate not valid at current time")
	ErrTreeFull     = errorsmod.Register(ModuleName, 7, "registry tree is full")
	ErrUnauthorized = errorsmod.Register(ModuleName, 8, "unauthorized")
	ErrDscRevoked   = errorsmod.Register(ModuleName, 10, "DSC has been revoked")
	ErrCscaRevoked  = errorsmod.Register(ModuleName, 11, "issuing CSCA has been revoked")
	// ErrTooManyIssuers means more than MaxIssuerCandidates trust-store
	// certificates named themselves as this DSC's issuer.
	ErrTooManyIssuers = errorsmod.Register(ModuleName, 13, "too many candidate issuing CSCAs")
	// ErrNotDsc means the certificate presented as a Document Signer is an
	// issuer's: a CA, a certificate-signing key, or self-issued.
	ErrNotDsc = errorsmod.Register(ModuleName, 14, "certificate is not a Document Signer")
)
