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
  the leg is pulled. x/dex note-path fixtures re-recorded.
- [x] 3 personhood / assembly
  - C3: InitGenesis refuses an identity root that is not the rebuilt
    tree's root at its tree_size (identityRootsAt: leaves [0, n), zero where
    no registration) or is dated after genesis; Validate refuses tree_size
    past the tree. Export keeps only verifiable roots (one from before a
    leaf was zeroed is dropped: an anchor only). Tests:
    x/personhood/keeper/audit4_genesis_root_test.go
  - C6: x/assembly GenesisState.proposal_subjects (6) exported and
    imported; a proposal in voting without an entry is still classified;
    Validate: unique ids, at most one of excluded_dsc (32 bytes) /
    excluded_country (alpha-2) / refusal. Test:
    x/assembly/keeper/audit4_subjects_test.go
  - C7: caretaker / referrer max_activation must be strictly before
    now - lease - margin (wallet-facing). TestCaretakerActivationBound
    updated; app personhood fixtures re-recorded (bound - 1).
  - C8: x/allocation AdvanceIndexTo(stream, t); the caretaker sweep
    settles the stream to each lapsed lease's expiry before clearing it,
    and runs first on its own budget (types.CaretakerSweepLimit = 1000).
    Test: x/allocation/keeper/audit4_advance_to_test.go;
    TestLargePurgeDoesNotStarveOtherSweeps updated.
  - C10: moot. Referrer consent v2 (expiry height) was implemented
    (167b586), then removed with public referrer addresses (see 7).
- [x] 4 buyback C5: window <= max trade <= accrual checked on the effective
  (default-resolved) values; randomising the trade block was considered
  and rejected (the proposer controls the hash inputs), documented in
  Params.Validate. Test: x/personhood/types/audit4_buyback_params_test.go
- [x] 5 core
  - L1: shieldedante.ExpiredTimeoutDecorator refuses timeout_height <=
    last committed height in CheckTx/ReCheckTx; PrepareProposal drops txs
    with timeout_height < req.Height before counting the private cap.
    Test: app/audit4_timeout_test.go
  - I1: max_private_actions_per_block doc rewritten (enforced by the ante,
    not ProcessProposal; PrepareProposal's conservative count; failing txs
    bounded by block gas).
  - I3: zk/orchard verifyRecovered: a verifier panic in a VerifyProofs
    worker (or the sequential path) is that action's ActionError
    (zk/orchard/bundle_recover_test.go); shieldedante.RecoverDecorator
    (right after SetUpContext) returns a private-ante panic as ErrPanic with
    the context whose meter carries the gas already charged
    (x/shielded/ante/guards_test.go).
  - Core PoCs ported: zk/ultrahonk/audit4_fuzz_test.go (proof element
    junk never crashes or verifies), zk/indexed/audit4_soundness_test.go
    (indexed tree non-membership soundness, deterministic).
- [x] 6 staking
  - G1 / L-B: initStakeTree checks every stake_roots record and every
    snapshot root against the rebuilt tree at its size, refuses Time >
    genesis time and sizes past the tree.
  - G2: InitGenesis refuses a withdraw address on the module account;
    ValidateOperatorWithdrawAddrs refuses one on any module account;
    invariant 5 checks it.
  - L-A: stake root and nf root recorded in one guarded call.
  - I1: snapshot supply is the supply at the start of the snapshot's
    block (ValidatorState.supply_height / supply_at_block_start;
    checkpoints split per group; zero-height export shifts supply_height).
    Test: x/shieldedstaking/keeper/audit4_supply_internal_test.go
  - I2: InitGenesis refuses a snapshot at or above the initial height.
  - Hardening: AfterValidatorCreated drops the validator from
    PendingReleases; guarded() re-panics OOG; StakeTally recovers errors
    and panics into an empty (failing) tally.
  - Tests: app/audit4_genesis_test.go (ported PoCs, refused).
- [x] 7 handles (user decision; replaces the referral-code feature of
  4b94dcd and public referrer addresses)
  - x/personhood Handle {handle, owner_pk, ek_pub, nullifier, expires_at}:
    claimed with MsgBindHandle (membership scope "handle", one per human,
    caretaker activation rule, lease caretaker_vote_seconds). Lifecycle:
    live -> renewal period (handle_renewal_seconds, default 30 days; owner
    only, does not resolve) -> free (swept in runSweeps, sweepHandles). A
    change frees the old handle at once; an empty bind releases at once.
  - MsgRegister affiliate_handle (15) + affiliate_pc (11) +
    affiliate_ciphertext (12): the referrer's half minted as a note there;
    binding affiliate field H(TAG_AFFILIATE, Bytes(handle), pc,
    Bytes(ct)). Transparent referral payout, MsgBindReferrer, consent,
    ReferrerBinding, Referrer query and referral codes removed.
  - Queries Handle, Handles (directory, start/limit); events handle_bound,
    handle_released; genesis handles (16).
  - Tests: x/personhood/keeper/handle_internal_test.go (every transition,
    directory paging, genesis round trip, sweep);
    TestRegistrationBinding (handle resolves; lapsed refused; handle / pc /
    ciphertext / none swaps break the binding); types canonical_test.go;
    app TestPrivatePersonhood (claim, relayer swaps refused, taken, renewal
    refused to another identity, unknown refused, C2 and D1 referral
    notes minted, address change, handle change frees the old one,
    directory, genesis round trip). Passports and app fixtures re-recorded.
- [x] 8 fixtures (dex note paths; personhood passports + app), make
  genesis, go build/vet/test ./... pass, make genesis-check passes (sha256
  24f883576256fd96dc4fdf3b49297fa0ac2f4f3bdac7fd19057720c1253bba99), make
  privacy-vks-check CIRCUITS=../mobile-orch/circuits passes (vks
  unchanged). Genesis changed only by the new empty genesis fields.
