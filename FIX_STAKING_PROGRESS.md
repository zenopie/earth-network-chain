# x/shieldedstaking audit fixes (branch fix/staking)

Base: privacy/orchard bae86fa. Auditor PoCs ported as regression tests in
app/audit_regression_test.go (each now asserts the fail-safe outcome).

## Status
- [x] 1 CRITICAL canonical validator strings (staking part; other modules: see below)
- [x] 2 HIGH epoch starvation (cursor, min delegation, empty books)
- [x] 3 MED-HIGH gov snapshot gas, AssertInvariants bound/guard
- [x] 4 MEDIUM zero-height export
- [x] 5 Lows (F4 donation, F5 orphan, positions, F6, F7, F8, F9)
- [x] build/vet/test ./..., make genesis-check (genesis.json rebuilt: min_delegation, min_position 100 ERTH, new fields). Staking fixtures: not regenerated, every proof the tests use is unchanged (app suite passes on the committed cache).

## Log
- F0 canonical valoper: types.CanonicalValoper (bech32, chain valoper prefix,
  20/32 bytes, string == lowercase re-encoding) in every msg's ValidateBasic;
  keeper.valAddr round-trips through the validator codec and refuses
  aliases (covers Delegate/Restake/Undelegate/Lock, Backing, epoch, queries);
  checkClaim/checkStakeVote call valAddr; queries Validator/UnbondRecord
  refuse; genesis Validate + InitGenesis refuse non-canonical books,
  records, positions, snapshots, votes. Tests: TestAuditValoperCaseAlias*,
  TestAuditGenesisRefusesNonCanonicalValoper, types TestCanonicalValoper.
- F1 sweep: epoch end starts EpochSweep{active,cursor}; EndBlocker continues
  it each block (EpochValidatorLimit books/block, key order from cursor).
  processValidator(v, maxEpoch) settles only records of ended epochs;
  PendingUndelegation -= settled targets. min_delegation param (1 ERTH):
  amount and minted derth. Test TestAuditEpochValidatorStarvation,
  TestAuditMinDelegation.
- F2 snapshots O(1): ProposalSnapshot.seq; supply via copy-on-write
  SupplyCheckpoints (validator, seq) written by Delegate/Undelegate before
  the first supply change after a snapshot; lookup = first entry >= seq,
  else current supply. Pruned below the oldest open seq. Legacy snapshots
  (seq 0) use their validators list. reportInvariants: bounded
  (InvariantBookLimit) + recover. Test TestAuditSnapshotGasIndependentOfBooks
  (18020 gas for 1 or 60 books), TestStakeVoteTally checks snapshot supply
  is unchanged by a later delegation.
- F3 zero-height: BookRewardsForZeroHeight + ResetHeightsForZeroHeight in
  prepForZeroHeightGenesis. Test TestAuditZeroHeightExportBreaksInvariants
  (export(true) -> InitGenesis on a fresh app; fails without the fix).
- F4 donation: harmless via min derth per delegation (loss <= 1e-6, else
  refused). Test TestAuditDonationInflationHarmless.
- F5 orphan: delegation to a book with S=0,B>0 refused ("settling"); at
  processing a settling book undelegates everything to its last records,
  or to an orphan record (payout -> community pool), queue -> community
  pool. Tests TestAuditOrphanBackingNotCaptured,
  TestAuditOrphanDelegationToCommunityPool.
- Positions: PositionCount O(1); default min_position 100 ERTH. SUPERSEDED
  2026-10-02: positions weighed per validator, no count/cap, min_position
  1 ERTH (FIX_SHIELDED_PROGRESS.md, G).
- F6 documented (votes.go).
- F7: BeforeDelegationRemoved(operator self-bond) schedules retirement;
  releaseRetiredEscrows pays the escrow after unbonding_time if no self
  delegation/unbonding. Test TestAuditEscrowReleasedOnRetirement.
- F8: MaturityLimit removed; max_entries >= ceil(unbonding/epoch)+1 checked
  at InitGenesis/UpdateParams; failed release -> PendingReleases retried at
  epoch end, invariant 5 aware.
- F9: UpdatePosition requires weight>0 with splits. Test
  TestAuditUpdatePositionNeedsWeight.
- Other modules (bech32 alias sweep): nothing exploitable (live state keyed
  by bytes). Hardened: allocation stores canonical recipient/claimer and
  compares the claimer by bytes (an uppercase stored claimer locked out the
  real one); genesis dedupe by lowercase for allocation voters, dex bids and
  LP unbondings, personhood referrer addresses (case pairs overwrote one
  another at InitGenesis, stranding weight/bids/shares); dex stores the
  canonical bidder / LP-unbonding address; proof-bound address strings
  (MsgSend.receiver, MsgRegister.affiliate, MsgBindReferrer.address) must be
  canonical (tx-hash malleability by relayers). Not changed: app
  genesis_withdraw.go foreign map and export.go jail allow-list (operator
  tooling, validation only).
- Undelegate refuses if this epoch's record is no longer PENDING
  (defensive: the sweep never settles the current epoch's records).
