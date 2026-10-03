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
- [x] R2 CheckTx bypass: action proofs verified before bundle proofs; CheckTx-only ultrahonk.VerifiedCache (shielded keeper: bundle + VerifyCircuit; personhood: passport proof). Test: app/reaudit_round2_test.go TestCheckTxValidBundleJunkActionProofCostsOne
- [x] R3 vesting validators: AfterValidatorCreated + initGenesisEscrows refuse a vesting operator (ErrVestingOperator); compoundSelfBond refuses (guarded, reverted) if the operator spendable moves. Tests: app/vesting_operator_test.go
- [x] R4 tally attribution: privateTally caps each validator deduction at voted x rate_now (Backing/supply now) in its shares. POC-B1 ported in app/shieldedstaking_test.go (realistic: shares and derth both grow)
- [x] R5 GwEpoch leak: syncValidatorVoter drops GwEpoch[v] when v has no live totals; epoch end no longer runs ReweighGroundworks, voters re-file per book in the bounded sweep (resyncBooks). Test: TestGroundworksIndexNoLeakAndEpochCostBounded
- [x] R6 proof chunk canonical: ultrahonk.Verify refuses any 32-byte proof element >= r (bb reduced them). Test: zk/ultrahonk/proof_canonical_test.go
- [x] R7 tx malleability: private txs refuse AuthInfo.tip and must be byte-equal to TxRaw{marshal(body with each msg re-marshaled), marshal(auth_info)} (ctx.TxBytes). Test: app/reaudit_round2_test.go TestPrivateTxRespellingsRefused
- [x] R8 max_entries/unbonding floor at epoch end: endEpoch reports a violated floor (epoch_failure stage unbonding_floor); processValidator defers an undelegation whose max_entries is full (event shieldedstaking_unbonding_deferred), records stay PENDING. Test: TestEpochEndDefersUndelegationPastMaxEntries
- [x] smaller items:
  - [x] registration_sweep_limit validated (R1 commit)
  - [x] removal cooldown: only a declined ballot grants it; a carried one
    (struck, or strike failed) removes/never sets the entry. Decision: a
    declined ballot opened by the option's own beneficiaries still grants
    30 days (the ballot is public for 7 days; anyone can carry it). Test:
    x/assembly TestCarriedRemovalGrantsNoCooldown
  - [x] assertEscrows bounded: reportInvariants counts reward escrows (x2)
    against InvariantBookLimit
  - [x] genesis exports pending_releases + retiring_escrows (imported:
    escrow re-recorded, retry at epoch end; retirement keeps its time)
  - [x] reweighSlashed: post-slash epoch rate = min(live rate, epoch rate)
  - [x] StakeProof.ciphertexts exactly 2, empty iff commitment zero
  - [x] exact lengths: bundle action ciphertext == 217 (types.NoteCiphertextBytes),
    StakeProof present ciphertext == 153 (privacy.WalletStakeCiphertextBytes).
    Tests: x/shielded/types (short/long/empty), x/shieldedstaking/types
    TestStakeProofCiphertextShape. Test helpers NoteCT/StakeCT; every proof
    fixture re-recorded (shielded script; staking, dex and personhood app
    fixtures by one proving run of ./app); fixture scripts' -run patterns
    now include the round-2 regression tests.

## Status

Disk freed. go build/vet/test ./... pass (no circuits needed); make
genesis-check passes (genesis regenerated for used_bindings,
pending_releases, retiring_escrows: sha256
c27341544f40c402a31a8b4de0339a057820904c953562beacc35456fd81f5c1);
make privacy-vks-check passes against ../mobile-orch/circuits (vks
unchanged). Every item above is tested: R2 (exactly 1 verification
per junk attempt), R4 (POC-B1: +100k ERTH post-snapshot stake goes to vB's
Yes; n1's abstain unchanged), R5 (epoch-end gas identical with 0 and 10,000
stale entries), R3, R8, removal cooldown.
