# Fix round 3 (chain audit 3) progress

PoCs: /private/tmp/claude-501/audit3/{core/chain,staking/repo,pdx/*}. Each is
ported as a regression test asserting the safe outcome.

- [x] 1 escrow poisoning (HIGH): compounding and every release move only
  the escrow's spendable coins (locked coins at the address stay, never
  fail anything); a pre-existing account at the escrow is not refused at
  creation (that would be a creation DoS). A failed retirement release
  moves EscrowRetryDelay (24h) back in its queue; pending-release retries
  rotate through the set from a cursor (PendingReleaseCursor, prefix 31).
  Tests: app/audit3_escrow_test.go TestAudit3EscrowPoisonedByLockedAccount
  (compounds, retirement released, 1 locked uerth left),
  TestAudit3FailingEscrowReleasesDoNotStarveQueues (60 always-failing
  entries in each queue; the good one behind them is released).
- [x] 2 unshield into module accounts: checkUnshield refuses any module
  account the app declares (auth GetModulePermissions, materialized or
  not); x/shielded's ReleaseToModule marks its context
  (shieldedtypes.WithModuleRelease(ctx, target)) and x/shieldedstaking's
  send restriction accepts pool -> module only under its own marker.
  Invariant 1 stays exact (refusal, not a sweep of surplus). Tests:
  app/audit3_unshield_module_test.go (two new proof fixtures for the
  unshield to the module; shielded-fixtures.sh -run covers it).
- [-] 3 stake votes on concurrent proposals: DROPPED by decision. A no-spend
  vote reveals the same nullifier on every vote and again at the later spend
  (linkable). Stake voting is left exactly as it is; a separate job replaces
  it with a per-proposal vote nullifier + indexed nullifier-tree
  non-membership circuit.
- [x] 4 gas-check blind to revocations (M1): remoteKV.Get re-queries an empty
  answer with prove=true and reads the ics23 exist/nonexist bit (fails closed
  without a proof). Test: cmd/earthd/cmd/gascheck_keyset_test.go
  TestPOC_GasCheckBlindToRevocations (real committed IAVL multistore).
- [x] 5 demoted expedited proposal loses subjects (M2): subjects forgotten in
  AfterProposalVotingPeriodEnded once the status is final (backstop in the
  bounded subjects sweep), not on chamber approval; subjectsOf refuses a
  voting proposal with no fixed subjects (ErrProposalNotVoting) rather than
  classifying per vote. Tests: x/assembly/keeper/audit3_demoted_subjects_test.go
  (PoC port: subjects survive demotion, 0 pki calls for 10 junk votes,
  forgotten after the regular round ends).
- [x] 6 mempool fork: app.New appends baseapp.SetMempool(NoOpMempool{}) last;
  a configured mempool.max-txs != -1 is logged and ignored. Test:
  app/audit3_mempool_test.go TestAudit3AppMempoolForcedNoOp (SenderNonce
  option given -> still no-op, same tx result).
- [x] 7 unbond record growth: orphan records indexed (OrphanRecords,
  prefix 32; rebuilt at InitGenesis from requested == 0); sweepOrphanRecords
  and hasOrphanRecords walk only the index. Open records were already
  indexed (PendingRecords, MaturityQueue). No per-validator record walk is
  left at the epoch end. Test: app/audit3_unbond_records_test.go (epoch-end
  gas identical with 1,100 and 6,100 unclaimed matured records; was
  1.67M vs 6.11M).
- [x] 8 vote weight canonical strings: ValidateOptions requires
  weight == LegacyDec.String() (18 decimals). Test:
  x/shieldedstaking/types/audit3_weights_test.go
- [x] 9 genesis root records: InitGenesis checks each record against the
  rebuilt tree at its tree_size, refuses Time > genesis time and two roots
  for one size. Tests: x/shielded/keeper/audit3_genesis_test.go
- [x] 10 personhood: L1 used binding held until proof current_date +
  MaxCurrentDateMaxSkewSeconds (1y) + 1d; I1 YYMMDD round-trip; L2 genesis
  exports pending_dsc_purges + rate counters; L4/L5 constant one-day
  activation margin (ActivationMarginSeconds) for ballots/rounds/leases +
  LeaseHold on lowered caretaker_vote_seconds (a per-round stored window
  was rejected as unsound: a raise after opening extends unpruned roots);
  L6 MsgBindReferrer consent signature (ErrNoReferrerConsent 1125). Tests:
  x/personhood/keeper/audit3_internal_test.go (ported PoCs),
  TestAudit3LoweredLeaseLengthHeld, TestAudit3ReferrerConsent;
  TestPrivatePersonhood re-timed. Passport + app fixtures re-recorded
  (scripts/personhood-fixtures.sh all).
- [x] 11 dex L3: deposit() refuses a zero pulled leg (ErrZeroShares). Test:
  x/dex/keeper/deposit_zero_leg_test.go TestAudit3DepositPullingZeroLegRefused.
  Buyback L7 (x/personhood): per-trade cap buyback_max_trade_seconds
  (default 3600, validated window..accrual cap), remainder carried
  (LastBuyback = now - carried). Tests: x/personhood/keeper/buyback_internal_test.go
  TestAudit3BuybackCapsTradePerWindow, TestAudit3BuybackMaxTradeValidated.
- [x] 12 info:
  - shieldedstaking module account has no Minter/Burner (it never mints or
    burns: stake notes live in its tree; payouts move by MintNote).
  - x/shieldedstaking InitGenesis enforces the delegation rule on what
    x/staking loaded without hooks: delegations and unbonding delegations
    only module or operator self-bond; no redelegations. Test:
    app/audit3_genesis_delegations_test.go.
  - F5: PrepareProposal wraps the SDK default (no-op mempool) and leaves out
    private txs past max_private_actions_per_block (conservative count; not
    checked in ProcessProposal). Test: app/audit3_prepare_proposal_test.go.

## Status

All items done except 3 (dropped by decision). go build/vet/test ./...
pass; make genesis-check passes (sha256
d5385d77cd1df472e049a78467bd896e143bd21009540b55fb578870e96154db); make
privacy-vks-check passes against ../mobile-orch/circuits (vks unchanged).
TestStakeVoteTally's residual third-party delegation is now refused at
import (the delegation rule); the test removes it before its round trip.
