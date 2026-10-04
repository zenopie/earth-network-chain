package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/personhood module sentinel errors
var (
	ErrInvalidSigner   = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrNoVerifyingKey  = errors.Register(ModuleName, 1101, "no registration verifying key configured")
	ErrInvalidProof    = errors.Register(ModuleName, 1102, "invalid registration proof")
	ErrBadPublicInputs = errors.Register(ModuleName, 1103, "proof public inputs do not match")
	ErrClaimTooSoon    = errors.Register(ModuleName, 1107, "already claimed today; the next claim opens at 00:00 UTC")
	// ErrRegistrationRateLimited is a deferral, not a rejection of the proof:
	// the registration is valid and the holder may retry once the day rolls.
	ErrRegistrationRateLimited = errors.Register(ModuleName, 1113, "daily registration limit reached for this document signer or country")
	// ErrDscRevoked is returned once governance has withdrawn trust from the
	// Document Signer a registration was made under.
	ErrDscRevoked = errors.Register(ModuleName, 1114, "the document signer behind this registration has been revoked")

	ErrInvalidMsg          = errors.Register(ModuleName, 1116, "invalid private msg")
	ErrUnknownIdentityRoot = errors.Register(ModuleName, 1117, "root is not a recent identity-tree root")
	ErrInvalidMembership   = errors.Register(ModuleName, 1118, "invalid membership proof")
	ErrWrongDay            = errors.Register(ModuleName, 1119, "claim is not for today")
	ErrIdentityTreeFull    = errors.Register(ModuleName, 1120, "identity tree full")
	ErrNoReferrer          = errors.Register(ModuleName, 1121, "affiliate_handle is not a live handle")
	ErrHandleTaken         = errors.Register(ModuleName, 1122, "handle is held by another human")
	// ErrRegistrationReplay: a switch to the identity commitment the live
	// registration already holds (a replayed MsgRegister).
	ErrRegistrationReplay = errors.Register(ModuleName, 1123, "passport is already registered to this identity commitment")
	// ErrBindingUsed: this registration (its exact binding: idc, notes,
	// ciphertexts, affiliate) has landed before. A replay of a public proof.
	ErrBindingUsed = errors.Register(ModuleName, 1124, "this registration has already been used")
	// ErrHandleMovedOut: this handle nullifier moved its handle away
	// (MsgMoveHandle) and may never hold another; or a move's new owner did.
	ErrHandleMovedOut = errors.Register(ModuleName, 1125, "this identity moved its handle away")
	// ErrCaretakerMovedOut: this caretaker nullifier moved its split away
	// (MsgMoveCaretaker) and may never cast another; or a move's new owner did.
	ErrCaretakerMovedOut = errors.Register(ModuleName, 1126, "this identity moved its caretaker split away")
	// ErrSwitchSignerMismatch: an identity switch whose proof is signed by a
	// different Document Signer from the live registration's. A re-proof of
	// the same passport is signed by the same signer.
	ErrSwitchSignerMismatch = errors.Register(ModuleName, 1127, "identity switch must be proven under the live registration's document signer")
)
