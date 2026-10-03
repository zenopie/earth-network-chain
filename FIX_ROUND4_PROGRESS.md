# Audit round 4: chain fixes

Branch privacy/orchard from 9b29f5d. PoCs: /private/tmp/claude-501/audit4/.
Each PoC is ported as a regression test asserting the safe outcome.

- [ ] 1 dex overflow halt (HIGH): reserve / LP-supply cap, big.Int payout and
  POL-burn math, recover() in per-entry cache branches; audit every
  Begin/EndBlock loop for unrecovered panics
- [ ] 2 dex C2 deposit legs rounded up; C4 drained 0/0 pools export/validate
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
