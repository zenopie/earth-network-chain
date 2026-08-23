package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrInvalidCert  = errorsmod.Register(ModuleName, 2, "invalid certificate")
	ErrNoIssuerCsca = errorsmod.Register(ModuleName, 3, "no trusted issuing CSCA found")
	ErrCertVerify   = errorsmod.Register(ModuleName, 4, "certificate signature verification failed")
	ErrCertExpired  = errorsmod.Register(ModuleName, 5, "certificate not valid at current time")
	ErrDuplicateDsc = errorsmod.Register(ModuleName, 6, "DSC already registered")
	ErrTreeFull     = errorsmod.Register(ModuleName, 7, "registry tree is full")
	ErrUnauthorized = errorsmod.Register(ModuleName, 8, "unauthorized")
	ErrUnknownDsc   = errorsmod.Register(ModuleName, 9, "DSC not found in registry")
	ErrDscRevoked   = errorsmod.Register(ModuleName, 10, "DSC has been revoked")
	ErrCscaRevoked  = errorsmod.Register(ModuleName, 11, "issuing CSCA has been revoked")
)
