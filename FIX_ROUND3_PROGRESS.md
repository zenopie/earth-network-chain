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
- [ ] 4 gas-check blind to revocations
- [ ] 5 demoted expedited proposal loses subjects
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
- [ ] 10 personhood L1/L2/L4/L5/L6/I1
- [ ] 11 dex L3 rounding / L7 buyback cap
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
