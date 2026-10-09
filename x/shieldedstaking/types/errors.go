package types

import "cosmossdk.io/errors"

// x/shieldedstaking sentinel errors. Codes 1105 and 1107 are retired: never
// reuse them.
var (
	ErrInvalidSigner   = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInvalidMsg      = errors.Register(ModuleName, 1101, "invalid private staking msg")
	ErrValidator       = errors.Register(ModuleName, 1102, "validator cannot take private delegations")
	ErrAmount          = errors.Register(ModuleName, 1103, "amount converts to nothing or overflows a note")
	ErrNotMatured      = errors.Register(ModuleName, 1104, "unbonding record is not open")
	ErrNoVoting        = errors.Register(ModuleName, 1106, "proposal is not open to stake votes")
	ErrGroundworksVote = errors.Register(ModuleName, 1108, "invalid groundworks vote")
	// 1109 is retired (ErrSignature: not the position's owner).
	ErrTransparentStaking  = errors.Register(ModuleName, 1110, "delegation is private on Earth: stake with the Earth Wallet (shielded staking); MsgDelegate, MsgUndelegate and MsgCancelUnbondingDelegation are only for a validator's own self-bond, and MsgBeginRedelegate is disabled")
	ErrSendRestricted      = errors.Register(ModuleName, 1111, "send refused by private staking's restriction")
	ErrInvariant           = errors.Register(ModuleName, 1112, "private staking invariant broken")
	ErrStakeTree           = errors.Register(ModuleName, 1113, "stake note tree refused the note or anchor")
	ErrStakeNullifierSpent = errors.Register(ModuleName, 1114, "stake nullifier already spent")
	ErrInvalidStakeProof   = errors.Register(ModuleName, 1115, "stake proof does not verify")
	ErrOperatorWithdraw    = errors.Register(ModuleName, 1116, "withdraw addresses cannot be changed on Earth: validator rewards and commission auto-compound into self-bond")
	ErrVoteNullifierUsed   = errors.Register(ModuleName, 1119, "this stake note already voted on this proposal")
	ErrVestingOperator     = errors.Register(ModuleName, 1118, "a vesting account cannot operate a validator: compounding its rewards would unlock its vesting coins")
	ErrRedelegation        = errors.Register(ModuleName, 1120, "redelegation refused")
	ErrOperatorRewardClaim = errors.Register(ModuleName, 1117, "validator rewards and commission auto-compound into self-bond and cannot be withdrawn: to take them out, undelegate the self-bond (the unbonding period applies)")
)
