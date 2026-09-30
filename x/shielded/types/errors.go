package types

import (
	"cosmossdk.io/errors"
)

// x/shielded module sentinel errors
var (
	ErrInvalidSigner       = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInvalidTransfer     = errors.Register(ModuleName, 1101, "invalid transfer")
	ErrInvalidProof        = errors.Register(ModuleName, 1102, "invalid proof")
	ErrUnknownRoot         = errors.Register(ModuleName, 1103, "root is not a recent note-tree root")
	ErrNullifierSpent      = errors.Register(ModuleName, 1104, "nullifier already spent")
	ErrAssetNotRegistered  = errors.Register(ModuleName, 1105, "denom is not admitted to the shielded pool")
	ErrUnauthorized        = errors.Register(ModuleName, 1106, "private msg did not pass the private ante chain")
	ErrMissingVerifyingKey = errors.Register(ModuleName, 1107, "no verifying key configured for circuit")
	ErrBlockCap            = errors.Register(ModuleName, 1108, "block private tx cap reached")
	ErrSendRestricted      = errors.Register(ModuleName, 1109, "send refused by the shielded pool's restriction")
	ErrTreeFull            = errors.Register(ModuleName, 1110, "note tree full")
	ErrInvalidNote         = errors.Register(ModuleName, 1111, "invalid note")
	ErrAlreadyReleased     = errors.Register(ModuleName, 1112, "transfer value already released")
	ErrInvariant           = errors.Register(ModuleName, 1113, "shielded pool invariant broken")
)
