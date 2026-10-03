# Audit round 5: chain fixes

Branch privacy/orchard from 4a663d5. Report: audit5-chain.md (scratchpad).
Fresh-genesis relaunch: no migrations.

- [x] A1 (H3) allocation: restoreStream adds an INTEGRATED option to
  IntegratedOptions only if not Removed; pruneOption removes the
  IntegratedOptions entry; resolveIntegrated drops a dangling entry on
  ErrNotFound. Tests: x/allocation/keeper/audit5_struck_integrated_test.go
- [ ] P1 (H1) referral note bound to the handle
- [ ] D1 (H2) LP payout legs above u64
- [ ] P2 (M1) one live handle per passport
- [ ] P3 (M2) lease bounds queryable
- [ ] Lows
- [ ] fixtures, VKs, genesis, CHANGELOG
