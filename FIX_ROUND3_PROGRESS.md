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
- [ ] 2 unshield into module accounts
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
- [ ] 7 unbond record growth
- [x] 8 vote weight canonical strings: ValidateOptions requires
  weight == LegacyDec.String() (18 decimals). Test:
  x/shieldedstaking/types/audit3_weights_test.go
- [x] 9 genesis root records: InitGenesis checks each record against the
  rebuilt tree at its tree_size, refuses Time > genesis time and two roots
  for one size. Tests: x/shielded/keeper/audit3_genesis_test.go
- [ ] 10 personhood L1/L2/L4/L5/L6/I1
- [ ] 11 dex L3 rounding / L7 buyback cap
- [ ] 12 info: shieldedstaking perms, genesis delegation rule, F5
