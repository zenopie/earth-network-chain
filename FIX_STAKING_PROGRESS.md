# x/shieldedstaking audit fixes (branch fix/staking)

Base: privacy/orchard bae86fa. Auditor PoCs ported as regression tests in
app/audit_regression_test.go (each now asserts the fail-safe outcome).

## Status
- [x] 1 CRITICAL canonical validator strings (staking part; other modules: see below)
- [ ] 2 HIGH epoch starvation (cursor, min delegation, empty books)
- [ ] 3 MED-HIGH gov snapshot gas, AssertInvariants bound/guard
- [ ] 4 MEDIUM zero-height export
- [ ] 5 Lows (F4 donation, F5 orphan, positions, F6, F7, F8, F9)
- [ ] fixtures regenerated, build/vet/test, genesis-check

## Log
- F0 canonical valoper: types.CanonicalValoper (bech32, chain valoper prefix,
  20/32 bytes, string == lowercase re-encoding) in every msg's ValidateBasic;
  keeper.valAddr round-trips through the validator codec and refuses
  aliases (covers Delegate/Restake/Undelegate/Lock, Backing, epoch, queries);
  checkClaim/checkStakeVote call valAddr; queries Validator/UnbondRecord
  refuse; genesis Validate + InitGenesis refuse non-canonical books,
  records, positions, snapshots, votes. Tests: TestAuditValoperCaseAlias*,
  TestAuditGenesisRefusesNonCanonicalValoper, types TestCanonicalValoper.
