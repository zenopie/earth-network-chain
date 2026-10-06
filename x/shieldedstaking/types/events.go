package types

// Events.
const (
	EventTypeDelegate   = "shieldedstaking_delegate"
	EventTypeUndelegate = "shieldedstaking_undelegate"
	// EventTypeRedelegate: derth moved between validators (the merged
	// derth/<dst> stake note's own event carries its position, commitment
	// and ciphertext).
	EventTypeRedelegate     = "shieldedstaking_redelegate"
	EventTypeEpoch          = "shieldedstaking_epoch"
	EventTypeEpochValidator = "shieldedstaking_epoch_validator"
	EventTypeEpochFailure   = "shieldedstaking_epoch_failure"
	// EventTypeUnbondingDeferred: an epoch end left a validator's
	// undelegation for the next one, its max_entries being full.
	EventTypeUnbondingDeferred = "shieldedstaking_unbonding_deferred"
	EventTypeMatured           = "shieldedstaking_matured"
	// EventTypeUnbondPayout: a private undelegation was paid out as notes
	// (their shielded_mint events, with the payout's pc's ciphertext,
	// precede it); EventTypeUnbondPayoutFailed: paying it failed, it is
	// kept and retried at retry_at.
	EventTypeUnbondPayout       = "shieldedstaking_unbond_payout"
	EventTypeUnbondPayoutFailed = "shieldedstaking_unbond_payout_failed"
	EventTypeSlashHaircut       = "shieldedstaking_slash_haircut"
	EventTypeStakeVote          = "shieldedstaking_stake_vote"
	EventTypeSnapshot           = "shieldedstaking_snapshot"
	EventTypePosition           = "shieldedstaking_position"
	EventTypeInvariant          = "shieldedstaking_invariant_broken"
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
	// (position, commitment, and the proof output's wallet stake
	// ciphertext), every spent nullifier, every recorded root.
	EventTypeStakeNote      = "shieldedstaking_stake_note"
	EventTypeStakeNullifier = "shieldedstaking_stake_nullifier"
	EventTypeStakeRoot      = "shieldedstaking_stake_root"
	// The slash debt of private redelegations (ORCHARD_DESIGN.md 8.7): a
	// slash reached the module's redelegation entries into a
	// validator (EventTypeSlashDebt: the derth taken off its supply), each
	// move it reached (EventTypeMoveSlashed: its debt and what its exposure
	// is still worth), and the debt tree's row for it (EventTypeDebtRow:
	// leaf index and the new root; the wallets' stream).
	EventTypeSlashDebt   = "shieldedstaking_slash_debt"
	EventTypeMoveSlashed = "shieldedstaking_move_slashed"
	EventTypeDebtRow     = "shieldedstaking_debt_row"

	AttributeKeyValidator   = "validator"
	AttributeKeyAmount      = "amount"
	AttributeKeyDerth       = "derth"
	AttributeKeyValue       = "value"
	AttributeKeyDenom       = "denom"
	AttributeKeyEpoch       = "epoch"
	AttributeKeyRate        = "rate"
	AttributeKeySupply      = "supply"
	AttributeKeyRewards     = "rewards"
	AttributeKeyDelegated   = "delegated"
	AttributeKeyUndelegated = "undelegated"
	AttributeKeyError       = "error"
	AttributeKeyStage       = "stage"
	AttributeKeyPayout      = "payout"
	AttributeKeyProposal    = "proposal_id"
	AttributeKeyWeight      = "weight"
	// AttributeKeySplitExpiresAt: a position's split lease end (unix
	// seconds, 0 without a split).
	AttributeKeySplitExpiresAt = "split_expires_at"
	AttributeKeyOptions        = "options"
	AttributeKeyPosition       = "position_id"
	AttributeKeyAction         = "action"
	AttributeKeyRoot           = "root"
	AttributeKeyFraction       = "fraction"
	AttributeKeyCommitment     = "commitment"
	AttributeKeyNullifier      = "nullifier"
	AttributeKeyCiphertext     = "ciphertext"
	AttributeKeyTreeSize       = "tree_size"
	AttributeKeyWithdrawAddr   = "withdraw_address"
	AttributeKeyIndex          = "index"
	AttributeKeyNfRoot         = "nf_root"
	AttributeKeyNfSize         = "nf_size"
	AttributeKeyVoteNFs        = "vote_nullifiers"
	AttributeKeyPayoutID       = "payout_id"
	AttributeKeyNotes          = "notes"
	AttributeKeyPositions      = "positions"
	AttributeKeyAttempts       = "attempts"
	AttributeKeyRetryAt        = "retry_at"
	AttributeKeySrcValidator   = "src_validator"
	AttributeKeyDstValidator   = "dst_validator"
	AttributeKeyCredited       = "credited"
	AttributeKeyMoveKey        = "move_key"
	AttributeKeyMoveTime       = "move_time"
	AttributeKeyRetained       = "retained"
	AttributeKeyDebt           = "debt"
	AttributeKeyEntries        = "entries"
	AttributeKeyQueued         = "queued"
	AttributeKeyBonded         = "bonded"
	AttributeKeyCompletion     = "completion_time"
)
