# Fix round 2 (chain re-audit) progress

PoCs: /private/tmp/claude-501/reaudit/chain/{B,C,D,stakingA}. Each is ported
as a regression test asserting the safe outcome.

- [x] R1 registration A->B->A replay: UsedBindings (binding -> expiry =
  now + current_date_max_skew_seconds + 1 day), refused in checkRegistration
  (shared by the ante and `earthd gas-check registration`), pruned as the fifth
  runSweeps sweep, in genesis (used_bindings). Test:
  x/personhood/keeper/registration_replay_internal_test.go, app TestPrivatePersonhood.
  Also: registration_sweep_limit validated (0 = default, else 5..10000),
  current_date_max_skew_seconds <= 1 year.
- [~] R2 CheckTx bypass: action proofs verified before bundle proofs; CheckTx-only ultrahonk.VerifiedCache (shielded keeper: bundle + VerifyCircuit; personhood: passport proof). Test: app/reaudit_round2_test.go TestCheckTxValidBundleJunkActionProofCostsOne (NOT YET RUN: disk full)
- [~] R3 vesting validators: AfterValidatorCreated + initGenesisEscrows refuse a vesting operator (ErrVestingOperator); compoundSelfBond refuses (guarded, reverted) if the operator spendable moves. Tests: app/vesting_operator_test.go (NOT YET RUN)
- [ ] R4 tally attribution
- [~] R5 GwEpoch leak: syncValidatorVoter drops GwEpoch[v] when v has no live totals; epoch end no longer runs ReweighGroundworks, voters re-file per book in the bounded sweep (resyncBooks). Test: TestGroundworksIndexNoLeakAndEpochCostBounded (NOT YET RUN)
- [x] R6 proof chunk canonical: ultrahonk.Verify refuses any 32-byte proof element >= r (bb reduced them). Test: zk/ultrahonk/proof_canonical_test.go
- [x] R7 tx malleability: private txs refuse AuthInfo.tip and must be byte-equal to TxRaw{marshal(body with each msg re-marshaled), marshal(auth_info)} (ctx.TxBytes). Test: app/reaudit_round2_test.go TestPrivateTxRespellingsRefused
- [ ] R8 max_entries/unbonding floor at epoch end
- [ ] smaller items
