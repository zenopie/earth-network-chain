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
- [~] R4 tally attribution: privateTally caps each validator deduction at voted x rate_now (Backing/supply now) in its shares. POC-B1 ported in app/shieldedstaking_test.go (realistic: shares and derth both grow) (NOT YET RUN)
- [~] R5 GwEpoch leak: syncValidatorVoter drops GwEpoch[v] when v has no live totals; epoch end no longer runs ReweighGroundworks, voters re-file per book in the bounded sweep (resyncBooks). Test: TestGroundworksIndexNoLeakAndEpochCostBounded (NOT YET RUN)
- [x] R6 proof chunk canonical: ultrahonk.Verify refuses any 32-byte proof element >= r (bb reduced them). Test: zk/ultrahonk/proof_canonical_test.go
- [x] R7 tx malleability: private txs refuse AuthInfo.tip and must be byte-equal to TxRaw{marshal(body with each msg re-marshaled), marshal(auth_info)} (ctx.TxBytes). Test: app/reaudit_round2_test.go TestPrivateTxRespellingsRefused
- [~] R8 max_entries/unbonding floor at epoch end: endEpoch reports a violated floor (epoch_failure stage unbonding_floor); processValidator defers an undelegation whose max_entries is full (event shieldedstaking_unbonding_deferred), records stay PENDING. Test: TestEpochEndDefersUndelegationPastMaxEntries (NOT YET RUN)
- [~] smaller items:
  - [x] registration_sweep_limit validated (R1 commit)
  - [~] removal cooldown: only a declined ballot grants it; a carried one
    (struck, or strike failed) removes/never sets the entry. Decision: a
    declined ballot opened by the option's own beneficiaries still grants
    30 days (the ballot is public for 7 days; anyone can carry it). Test:
    x/assembly TestCarriedRemovalGrantsNoCooldown (NOT YET RUN)
  - [~] assertEscrows bounded: reportInvariants counts reward escrows (x2)
    against InvariantBookLimit
  - [~] genesis exports pending_releases + retiring_escrows (imported:
    escrow re-recorded, retry at epoch end; retirement keeps its time)
  - [~] reweighSlashed: post-slash epoch rate = min(live rate, epoch rate)
  - [~] StakeProof.ciphertexts exactly 2, empty iff commitment zero
  - [ ] exact lengths (stake output ct == 153, bundle action ct == 217):
    NOT DONE. The sighash binds every ciphertext, and every app/keeper
    fixture uses short placeholder ciphertexts ("plan-ct:..",
    "stake-ct:.."), so enforcing lengths means re-recording every proof
    fixture (scripts/*-fixtures.sh with EARTH_CIRCUITS) -- blocked by the
    full disk.

## BLOCKER: disk full

/System/Volumes/Data at 100% (~150 MB free); ~/Library/Caches/go-build is
29 GB. Linking any test binary (and since then even compiling some
packages) fails with ENOSPC. Pruning the shared go build cache was refused
by the permission classifier. Everything marked [~] is committed but NOT
compiled-and-tested end to end (R2..R8 type-checked with go vet before the
disk filled, except the last two commits). Next: free disk (e.g.
`go clean -cache`), then go build/vet/test ./..., fix fallout, make
genesis-check (genesis changed: personhood used_bindings, shieldedstaking
pending_releases/retiring_escrows are new empty fields), make
privacy-vks-check.
