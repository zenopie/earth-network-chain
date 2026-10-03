# Fix round 3 (chain audit 3) progress

PoCs: /private/tmp/claude-501/audit3/{core/chain,staking/repo,pdx/*}. Each is
ported as a regression test asserting the safe outcome.

- [ ] 1 escrow poisoning (HIGH)
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
- [ ] 8 vote weight canonical strings
- [ ] 9 genesis root records
- [ ] 10 personhood L1/L2/L4/L5/L6/I1
- [ ] 11 dex L3 rounding / L7 buyback cap
- [ ] 12 info: shieldedstaking perms, genesis delegation rule, F5
