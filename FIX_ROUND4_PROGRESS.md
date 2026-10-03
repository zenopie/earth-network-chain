# Audit round 4: chain fixes

Branch privacy/orchard from 9b29f5d. PoCs: /private/tmp/claude-501/audit4/.
Each PoC is ported as a regression test asserting the safe outcome.

- [x] 1 dex overflow halt (HIGH): types.MaxPoolAmount = 2^120 caps reserves
  (SetPool, genesis Validate), LP supply and inputs (create, deposit, swap
  and each hop, auction bid); mulDiv/mulDivUp in big.Int for payout, POL
  retirement (and retirableAt), deposit, auction claim; initialShares in
  big.Int. internal/safeexec (Cached/Item: cache branch + recover, OOG
  re-panicked) on every per-entry hook loop: dex unbonding sweep, stale-pool
  sweep, POL schedules, auction settlement, volume rebase; personhood
  sweeps (per sweep and per registration/caretaker entry) and buyback;
  allocation integrated handlers (resolved checked <= accumulated), option
  prune, residue sink, slashed resync; assembly per due proposal (failure
  refuses the proposal: failUnresolved), removal strike, failProposal's
  stake tally (failure: empty result, refund); shieldedstaking guarded()
  now safeexec (re-panics OOG). Left alone: invariant assertions (their
  halt is intended), x/shielded root record/prune (tree ops, no Int maths),
  x/earth fee split (per-denom halves of a balance). Tests:
  x/dex/keeper/audit4_test.go TestAudit4HugeVoucherPoolRefused,
  TestAudit4UnbondingPayoutOverflowNoHalt; internal/safeexec TestCached
- [x] 2 dex C2 deposit legs rounded up (mulDivUp; never above what was
  offered); a pool with shares but a zero reserve refuses deposits. C4:
  genesis Validate accepts zero (not negative) reserves; export of a 0/0
  pool validates and imports. Tests: TestAudit4DepositLegsRoundedUp,
  TestAudit4DrainedPoolExportValidates; audit-3 zero-leg test now asserts
  the leg is pulled
- [ ] 3 personhood C3 genesis identity roots; assembly C6 ProposalSubjects
  export; C7 activation boundary >=; C8 caretaker weight checks lease expiry
  live; C10 referrer consent expiry height
- [ ] 4 buyback C5 buyback_max_trade_seconds vs TWAP window
- [ ] 5 core L1 timeout_height (private ante + PrepareProposal); I1 cap param
  doc; I3 recover in VerifyProofs workers, private ante panics consume gas
- [ ] 6 staking genesis G1/L-B, G2 module withdraw addr, L-A paired roots,
  I1 supply snapshot H-1, I2 open snapshots on import; hardening
  (PendingReleases, guarded OOG, gov EndBlocker Backing guard)
- [ ] 7 fixtures, genesis, build/vet/test, genesis-check, privacy-vks-check
