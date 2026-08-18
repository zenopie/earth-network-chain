package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/personhood module sentinel errors
var (
	ErrInvalidSigner    = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrNoVerifyingKey   = errors.Register(ModuleName, 1101, "no registration verifying key configured")
	ErrInvalidProof     = errors.Register(ModuleName, 1102, "invalid registration proof")
	ErrBadPublicInputs  = errors.Register(ModuleName, 1103, "proof public inputs do not match")
	ErrAlreadyReg       = errors.Register(ModuleName, 1104, "this person is already registered")
	ErrNotRegistered    = errors.Register(ModuleName, 1105, "address is not a registered human")
	ErrRegExpired       = errors.Register(ModuleName, 1106, "registration has expired")
	ErrClaimTooSoon     = errors.Register(ModuleName, 1107, "already claimed today; the next claim opens at 00:00 UTC")
	ErrInvalidAffiliate = errors.Register(ModuleName, 1112, "invalid affiliate")
)
