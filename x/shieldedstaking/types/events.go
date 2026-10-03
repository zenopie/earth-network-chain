package types

// Events.
const (
	EventTypeDelegate       = "shieldedstaking_delegate"
	EventTypeUndelegate     = "shieldedstaking_undelegate"
	EventTypeClaim          = "shieldedstaking_claim"
	EventTypeEpoch          = "shieldedstaking_epoch"
	EventTypeEpochValidator = "shieldedstaking_epoch_validator"
	EventTypeEpochFailure   = "shieldedstaking_epoch_failure"
	// EventTypeUnbondingDeferred: an epoch end left a validator's
	// undelegation for the next one, its max_entries being full.
	EventTypeUnbondingDeferred = "shieldedstaking_unbonding_deferred"
	EventTypeMatured           = "shieldedstaking_matured"
	EventTypeSlashHaircut      = "shieldedstaking_slash_haircut"
	EventTypeStakeVote         = "shieldedstaking_stake_vote"
	EventTypeSnapshot          = "shieldedstaking_snapshot"
	EventTypePosition          = "shieldedstaking_position"
	EventTypeInvariant         = "shieldedstaking_invariant_broken"
	// EventTypeOrphan: uerth at a validator that no derth and no record owns
	// went to the community pool.
	EventTypeOrphan   = "shieldedstaking_orphan_to_community_pool"
	EventTypeSelfBond = "shieldedstaking_self_bond_compounded"
	// A removed validator's reward escrow was released to its operator.
	EventTypeEscrowReleased = "shieldedstaking_reward_escrow_released"
	// An operator's withdraw address pointed elsewhere than its reward escrow
	// at epoch end (set by a route the refusals do not reach) and was reset
	// to the escrow.
	EventTypeWithdrawAddrReset = "shieldedstaking_withdraw_addr_reset"
	// The stake note tree's stream, for wallets and indexers: every append
	// (position, commitment; a minted note's denom, amount and stake pc, a
	// created note's ciphertext), every spent nullifier, every recorded root.
	EventTypeStakeNote      = "shieldedstaking_stake_note"
	EventTypeStakeNullifier = "shieldedstaking_stake_nullifier"
	EventTypeStakeRoot      = "shieldedstaking_stake_root"

	AttributeKeyValidator    = "validator"
	AttributeKeyAmount       = "amount"
	AttributeKeyDerth        = "derth"
	AttributeKeyValue        = "value"
	AttributeKeyDenom        = "denom"
	AttributeKeyEpoch        = "epoch"
	AttributeKeyRate         = "rate"
	AttributeKeySupply       = "supply"
	AttributeKeyRewards      = "rewards"
	AttributeKeyDelegated    = "delegated"
	AttributeKeyUndelegated  = "undelegated"
	AttributeKeyError        = "error"
	AttributeKeyStage        = "stage"
	AttributeKeyPayout       = "payout"
	AttributeKeyProposal     = "proposal_id"
	AttributeKeyWeight       = "weight"
	AttributeKeyOptions      = "options"
	AttributeKeyPosition     = "position_id"
	AttributeKeyAction       = "action"
	AttributeKeyRoot         = "root"
	AttributeKeyFraction     = "fraction"
	AttributeKeyCommitment   = "commitment"
	AttributeKeySpc          = "spc"
	AttributeKeyNullifier    = "nullifier"
	AttributeKeyCiphertext   = "ciphertext"
	AttributeKeyTreeSize     = "tree_size"
	AttributeKeyWithdrawAddr = "withdraw_address"
)
