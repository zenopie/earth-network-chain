package types

import "cosmossdk.io/errors"

// x/shieldedstaking sentinel errors
var (
	ErrInvalidSigner       = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInvalidMsg          = errors.Register(ModuleName, 1101, "invalid private staking msg")
	ErrValidator           = errors.Register(ModuleName, 1102, "validator cannot take private delegations")
	ErrAmount              = errors.Register(ModuleName, 1103, "amount converts to nothing or overflows a note")
	ErrNotMatured          = errors.Register(ModuleName, 1104, "unbonding claim has not matured")
	ErrUnknownRecord       = errors.Register(ModuleName, 1105, "no such unbonding record")
	ErrNoVoting            = errors.Register(ModuleName, 1106, "proposal is not open to stake votes")
	ErrNoteSpent           = errors.Register(ModuleName, 1107, "note already spent")
	ErrPosition            = errors.Register(ModuleName, 1108, "invalid position")
	ErrSignature           = errors.Register(ModuleName, 1109, "not the position's owner")
	ErrTransparentStaking  = errors.Register(ModuleName, 1110, "transparent delegation is disabled: stake privately with x/shieldedstaking")
	ErrSendRestricted      = errors.Register(ModuleName, 1111, "send refused by private staking's restriction")
	ErrInvariant           = errors.Register(ModuleName, 1112, "private staking invariant broken")
	ErrStakeTree           = errors.Register(ModuleName, 1113, "stake note tree refused the note or anchor")
	ErrStakeNullifierSpent = errors.Register(ModuleName, 1114, "stake nullifier already spent")
	ErrInvalidStakeProof   = errors.Register(ModuleName, 1115, "stake proof does not verify")
)
