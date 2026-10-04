# Changelog

What changed in each release, for someone deciding whether to restart a
validator.

Entries are written for operators, not for the commit log. If a change means a
node behaves differently, needs a config edit, or must not be skipped, it says
so. The commit history has the reasoning.

This project follows [semantic versioning](https://semver.org). For a chain that
means: **any consensus-affecting change is breaking**, whatever the diff looks
like, because nodes running different versions cannot agree.

## [Unreleased]

**Not consensus-affecting.**

- x/shieldedstaking: **Query/Validators** (`/earth/shieldedstaking/v1/validators`,
  paged): every validator's quote inputs in one list (x/staking's validator,
  tombstoned, delegatable and refusal, the book, B, S, rate, D, W, the
  module's redelegation entries per destination). Wallets read the whole list
  instead of asking about one validator before a staking msg.
- Genesis: `scripts/ceremony.sh --genesis-time <RFC3339> --pubkey <json>`
  replaces `scripts/ceremony-gentx.sh` and does the whole launch ceremony
  (operator earth1n6amvk…, devnet faucet and gas wallet removed, gentx,
  genesis_time, rebuild). Not yet run: `TestLaunchCeremony` reports PENDING
  CEREMONY until it is.

**Consensus-affecting.**

- Passport registration covers every signature scheme unexpired passports
  use: 33 register circuits (RSA-2048/3072/4096 PKCS#1 v1.5 or PSS with any
  exponent in [3, 2^17); ECDSA on P-224/256/384/521 and brainpoolP224r1/
  256r1/384r1/512r1; SHA-1 to SHA-512 in the hash profiles real passports
  carry), one verifying key each in genesis (`signature_algorithm` is the
  variant id). DSC commitments: new tags P-224 8 and brainpoolP224r1 9; RSA
  commits its exponent under tag 10 (tag 7 retired). x/pki: explicit curve
  parameters name a curve only when all match; RSA-PSS with a mask other
  than MGF1 over the message hash, or a trailer other than 1, is refused;
  sha224WithRSAEncryption accepted. New genesis (no state migration).

- Stake vote padding (**new vote circuit and verifying key; genesis.json
  sha256 84921c0b360c3b3da84dd9c136481536fecae8dc503eada6ede475927cb77acc**).
  An unused vote slot publishes a padding nullifier
  H(TAG_VPAD = "earth.vpad", nk, r, proposal_id) instead of 0, so every vote
  looks the same. MsgStakeVote must carry exactly two non-zero, distinct
  vote nullifiers; both are recorded as used (a zero is refused). Its gas is
  1 + 2 note writes for every vote. Wallets must use the new vote.json.

- One stake note per validator; redelegation slashes paid by the notes they
  credited (user decisions; ORCHARD_DESIGN.md 8.1 and 8.7,
  AUDIT_HISTORY.md). **New stake and vote circuits and
  verifying keys; genesis.json sha256
  ffb269c5047e823b3f3aa27034767ff894c9fe76626703ccf42d0b1b321b6b59.**
  - x/shieldedstaking: **the chain mints no stake note.** The stake proof
    (circuits/stake v2, two lanes, 16 public inputs) merges the credited
    derth into the owner's existing note. **MsgDelegate.derth** (6) and
    **MsgRedelegate.dst_derth** (6) name the credit; the chain refuses one the
    value does not buy at the live rate (or below min_delegation), the rest
    staying in the book. StakeProof: `commitment` (9), `ciphertext` (10),
    `credit_nullifier/commitment/ciphertext` (11-13), `clear_before` (14),
    `debt_root` (15); `commitments`, `ciphertexts`, `spc_mint`,
    `spc_ciphertext` (4, 5, 6, 8) reserved. Note-moving msgs must spend in
    their first slot (a padding nullifier when the owner has no note) and
    create a note (a zero note when nothing is left). Wallet stake
    ciphertexts are 201 bytes. Restake only merges.
  - x/shieldedstaking: **MsgRedelegate no longer calls BeginRedelegate**:
    Unbond at src, Delegate at dst and the entry recorded by the module (one
    per block per pair, at most 1,024 per pair, then merging the oldest): no
    transitive refusal, no max_entries. **MsgRedelegate.move_time** (7, within
    600 s before the block). The credit is labelled with the move inside its
    note; until the move's entry matures the exposure cannot leave the note.
    A slash of src for an infraction before the move: the module takes the
    derth the burnt value backed off dst's supply (`ValidatorState.slash_debt`,
    9; dst's rate unchanged) and the moves owe it as rows of the new slash
    debt tree; a label clears at its row's retained value. New
    Query/DebtTree and Query/Move; Query/Redelegation removed. Events
    `shieldedstaking_slash_debt`, `shieldedstaking_move_slashed`,
    `shieldedstaking_debt_row`; `shieldedstaking_redelegate` has `credited`
    (was `minted`), `move_key`, `move_time`. Genesis `moves` (19),
    `debt_rows` (20), `max_unbonding_seconds` (21). Invariant 10.
  - x/shieldedstaking: **MsgStakeVote votes up to two notes**
    (`vote_nullifiers` exactly 2) and names `debt_root` (11, the current one;
    also in the sighash); a labelled note votes its value after slashes.
    9 public inputs.
  - x/shieldedstaking: the stake tree records its empty root at the first
    block. Both books of a redelegation, and the source of a slashed
    redelegation, are re-weighed in the same block; positions' splits drop
    options pruned since (on export; the re-filed voter at once).
  - x/allocation: **Groundworks weight counts a self-bond only at a Bonded
    validator** (audit 7 D7-L2); operators are resynced when their validator
    bonds or starts unbonding, their vote kept at weight zero meanwhile.
  - app: **the gov module account is on the blocked-address list** (audit 7
    D7-L1).
  - x/shieldedstaking, audit 7 (staking) fixes (ORCHARD_DESIGN.md 20.11). No
    circuit, verifying key or genesis change.
    - **A redelegation leaves the source's book pro rata** (A7-1): floor(value
      x P / (D - U + P)) out of the source's queue, the rest bonded with an
      x/staking entry, a move and its label. Out of the queue first only when
      the source is Unbonded or holds no bonded module stake. (It came out of
      the queue first: a mover who saw a slash coming escaped its share.)
    - **At 1,024 entries of positive height per pair** (was 4,096), a bonded
      move merges the two oldest entries (later height, earlier completion,
      their moves re-filed) and adds its own; joining the latest entry is
      only a last resort. MsgRedelegate's gas grows with the pair's entries
      (2,500 each, more at the cap).
    - **Which entries a slash reached is replayed** from x/staking's rule with
      x/slashing's two slash fractions (A7-L2): correct whatever fraction
      governance sets.
    - **Zero-height export with open moves re-imports** (A7-2): the moves are
      dropped (their entries are at height 0), the debt rows kept; genesis
      and invariant 9 skip shares and duplicate checks at height <= 0.
    - **Every stake proof must name the current clear_before** (within 3,600 s
      below Query/DebtTree's) **and the current debt_root**, whether or not it
      clears a label (audit B L-1): a proof that clears looks like any other.
      clear_before 0 is refused.
    - Genesis: a move's completion must match its entry's, every unmatured
      move needs its entry, and debt-row and move keys must be spent stake
      nullifiers. The epoch-end invariant pass decodes each redelegation once
      and counts moves and entries against its bound.

- Private redelegation (user decision, the one exception to the feature
  freeze; HISTORY.md, AUDIT_HISTORY.md). No circuit,
  verifying key or genesis change (genesis.json sha256
  1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8).
  - x/shieldedstaking: **new MsgRedelegate** {bundle, src_validator (2),
    dst_validator (3), amount (4), stake (5)}; sighash: StakeFields(stake),
    Bytes(src_validator), Bytes(dst_validator), amount. A stake proof spends
    derth/<src> (v_out = amount, change back); the chain moves the live
    value to dst (out of src's delegation queue first, the rest with
    x/staking's BeginRedelegate in the same block: no unbonding) and mints
    derth/<dst> at dst's live rate to spc_mint (spc_ciphertext required).
    Runs in the private ante, atomically with the spend: a refusal spends
    and pays nothing. Response {value, derth, position, completion_time}.
    Event `shieldedstaking_redelegate` (src_validator, dst_validator, derth,
    value, minted, queued, bonded, completion_time). Error 1120
    ErrRedelegation (same validator, x/staking's transitive refusal or
    max_entries per pair: the module is one delegator, so these limits are
    shared by every private staker). Gas 700,000 + proof + 7 note writes.
  - x/shieldedstaking: new `Query/Redelegation` {src_validator,
    dst_validator} -> src_locked_until, entries, max_entries,
    pair_frees_at, queue (`/earth/shieldedstaking/v1/redelegation/{src}/{dst}`).
  - x/shieldedstaking: **a slash of a redelegation's source reaches the
    destination's book pro rata** (slash_fraction x the entry's shares from
    the module's delegation at dst). The module's unbonding delegation at
    each destination is set aside for the slash and put back after it, so
    x/staking cannot take the slash from dst's undelegations under way (it
    would, first, and still slash the delegation in full). The module now
    has a **BeginBlocker** (ordered right after x/slashing and x/evidence)
    that puts them back before any tx.
  - x/shieldedstaking: genesis accepts the module's x/staking redelegations
    in flight and still refuses any other; invariant 9 checks them and that
    nothing stays set aside. Validator self-bonds still cannot redelegate.

- Staking without background transactions (user decision; see
  AUDIT_HISTORY.md, HISTORY.md). **New vote
  verifying key; genesis.json sha256
  1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8.**
  - x/shieldedstaking: **an undelegation pays out by itself.**
    MsgUndelegate carries `pc` (6) and `ciphertext` (7) (sighash: ..., amount,
    pc, Bytes(ciphertext)); no stake note is minted (`spc_ciphertext` must be
    empty). At maturity the chain mints `value x payout / requested` ERTH to
    pc as pool notes (2^63-1 each), at most 50 payouts and 256 notes a
    block, a failed payout kept and retried (1h << attempts-1, capped at
    256h), never dropped. Slashing reaches payouts exactly as it reached
    claims. Events `shieldedstaking_unbond_payout` (payout_id, validator,
    epoch, value, amount, notes, positions) and
    `shieldedstaking_unbond_payout_failed`; `shieldedstaking_undelegate`
    adds epoch and payout_id, drops denom; `shieldedstaking_matured` carries
    validator and epoch instead of denom. MsgUndelegateResponse: payout_id
    (4); denom (1) and position (3) reserved. New Query/UnbondPayout.
    Genesis: unbond_payouts (17), next_unbond_payout_id (18).
  - x/shieldedstaking: **MsgClaimUnbonding is retired**, with the
    `unbond/<valoper>/<epoch>` claim notes and denom.
  - x/shieldedstaking: **one stake vote per person.** MsgStakeVote votes up
    to four notes of one owner at one validator with one weight (their sum,
    three significant digits): `vote_nullifiers` (10), exactly four, used
    ones first, zeros after; `vote_nullifier` (9) reserved. The vote circuit
    (2^15) has 10 public inputs. Every used vote nullifier is refused on a
    second vote. StakeVote gains `vote_nullifiers` (7); the
    `shieldedstaking_stake_vote` event's `vote_nullifier` becomes
    `vote_nullifiers` (comma-separated hex). Positions vote as before.

- Audit round 6 (see AUDIT_HISTORY.md).
  No circuit, verifying key or genesis change (genesis.json sha256
  77af758697b293eb95d1f9f08a8f49b35bef648fd931ae1448cc3d0f2ddd652d):
  - x/allocation: **a self-bond withdrawn from a validator in any status
    takes its Groundworks weight with it.** It used to keep the weight when
    the validator was not Bonded (just created, jailed, unbonding), so
    create, vote and undelegate in one block left full weight with no
    stake. Voter splits drop options pruned since they were cast (at the
    next resync and in the export).
  - x/personhood: **the registration binding includes the chain id**
    (`H(TAG_REG, Bytes(chain_id), idc, ...)`); wallets must compute it. An
    identity switch proven under a different Document Signer from the live
    registration's is refused (code 1127); a switch counts against its
    signer's daily cap. A recurring identity root no longer leaves a stale
    by-time entry (export failed, the anchor expired early). Genesis
    refuses records dated after genesis and handle leases past genesis +
    handle_lease_max, and checks registration nullifier length. A
    registration the expiry or purge sweep cannot retire is passed over for
    a day instead of blocking the sweep.
  - x/personhood: `HandleEntry.owner` (Query/Handle, Query/Handles): the
    handle-scope nullifier holding the handle, hex. `handle_bound`,
    `handle_moved` and `handle_released` carry `owner`; `handle_moved` also
    `previous_owner`.
  - x/shielded: a private tx's gas_limit is at most 5x the gas it uses;
    every action of a bundle uses the same anchor; a send-disabled denom is
    refused on module releases and module mints too;
    `max_private_actions_per_block` is at most 256.
  - x/shieldedstaking: MsgStakeVote weight has at most three significant
    digits; LockPosition refuses more than 2^63-1; a slash of a validator
    with no book writes none; a snapshot after a failed root recording takes
    no roots; genesis requires a book for every module delegation.
  - x/dex: an LP payout that would pass the sweep's note budget waits;
    `volume_depth_cap_per_day` is at most 1,000. x/dex and x/personhood
    module accounts lose the unused Staking permission.
  - x/assembly: the Subjects sweep resumes from a cursor.
  - Not changed: transparent tx replay across earth-1 relaunches (X-1) is
    accepted; genesis account numbers are not offset.

- Audit round 5 (see AUDIT_HISTORY.md):
  - x/personhood: **the chain mints the referral note** to the referrer
    handle's registered address, with an opening derived from the passport
    nullifier and leaf index (`H("earth.referral", nullifier, leaf_index,
    0|1)`) and published on its `shielded_mint` event (`owner_pk`, `rho`,
    `rcm`; no ciphertext). MsgRegister drops `affiliate_pc` (11) and
    `affiliate_ciphertext` (12); the binding's affiliate field is
    `H("earth.affiliate", Bytes(handle))`. The `register` event adds
    `handle`, `referral`, `referral_position`. Before this a registrant
    could pay the referral half to itself.
  - x/personhood: a handle renewal or change by a holder whose handle is not
    live is bounded like a claim; MsgMoveHandle refuses a handle that is not
    live; a lapsed, unswept caretaker split no longer counts as held. Closes
    two live handles per passport via switch-and-switch-back.
  - x/personhood: new `Query/LeaseBounds` (effective handle and caretaker
    lease lengths, and the predecessor bounds at this block). Handle binds
    now cost nine note writes of gas.
  - x/dex: a private LP payout leg above 2^63-1 is minted as up to 128
    notes of at most 2^63-1; a failed payout is kept and retried with
    backoff (LpUnbonding `payout_attempts` = 10), never dropped; withdrawals
    whose note leg is above 32 notes' worth (32 x (2^63-1)) are refused at
    start.
  - x/shielded, x/shieldedstaking: **no chain-minted note above 2^63-1**
    (`MaxNoteValue`, what every wallet holds): MintNote, MintOpenNote,
    MsgShield, stake notes (delegation derth, undelegation claim, unlock),
    and a genesis position's derth. Swap fee rounds up. TWAP
    accumulators are exported (genesis `price_accumulators` = 11).
  - **New action, stake and vote verifying keys** (genesis): the circuits
    bound every note value to 2^63-1 (`note_cm`/`stake_cm`), so a bundle or
    stake proof can no longer create a note above `MaxNoteValue`. The
    action circuit had range-checked an output only as a u64, so a bundle
    could create a note of up to 2^64-1 that no wallet sees. Proofs from
    the old circuits do not verify; wallets must ship the new circuits.
    genesis.json sha256 77af758697b293eb95d1f9f08a8f49b35bef648fd931ae1448cc3d0f2ddd652d.
  - x/allocation: a struck INTEGRATED option stays out of the handler set
    across export/import, prune clears it, and a dangling entry no longer
    halts BeginBlock.
  - x/shielded: CheckTx/ReCheckTx refuse an anchor lapsing within 120 s;
    PrepareProposal leaves out txs whose anchor lapsed by the block's time;
    MsgShield refuses a send-disabled denom; gas prices capped (proof 10M,
    note 1M, bundle 1M); `MintNoteSplit`, `MintOpenNote`.
  - x/assembly: a chamber-ratified expedited proposal demoted by x/gov
    votes in a new round (new nullifier scope); ProposalTally uses the
    expedited bar; stale ballots are swept (with a cursor) and not imported.
  - app: the chamber's vote msgs pass the circuit breaker (ante and msg
    router).
  - x/shieldedstaking: checkpointSupply's walk is bounded; InitGenesis
    validates first; a position's derth must fit a u64.
- Audit round 4 (see AUDIT_HISTORY.md):
  - x/dex: **pool cap 2^120** on reserves, LP share supply, a swap input and
    the auction raise (ErrPoolCap, code 1120); payout / POL / deposit maths
    in big.Int. Before this an IBC voucher of 2^200 seeded a pool whose
    withdrawal payout overflowed math.Int in the EndBlocker: a permanent
    halt. Deposits now pull each leg rounded **up**; a pool drained to 0/0
    exports and validates.
  - Block hooks (all modules): per-entry work runs on a cache branch that
    recovers panics (internal/safeexec); a failing entry is skipped or
    dropped, not a halt. x/assembly refuses a proposal it cannot resolve;
    x/shieldedstaking's stake tally fails safe (empty result).
  - Genesis: x/personhood identity roots and x/shieldedstaking stake /
    snapshot roots are checked against the rebuilt trees and must not
    postdate genesis; x/shieldedstaking refuses open snapshots at or above
    the initial height and any withdraw address on its module account
    (also `genesis validate`, for every module account, and invariant 5);
    x/assembly exports proposal subjects (`proposal_subjects`).
  - x/shieldedstaking: a snapshot's derth supply is the supply at the start
    of its block (new ValidatorState fields `supply_height`,
    `supply_at_block_start`); stake and nullifier roots are recorded in one
    guarded call.
  - x/personhood: lapsed caretaker leases stop earning at their expiry
    (x/allocation AdvanceIndexTo; own sweep budget); buyback params need
    window <= max trade <= accrual on the effective values.
  - Core: CheckTx/ReCheckTx refuse a private tx whose timeout_height is at
    or below the last committed height; PrepareProposal drops expired txs
    before counting the private cap; a verifier panic in a proof worker is
    an action error; a panic in the private ante keeps the gas it charged.
- **Predecessor-aware activation** (membership circuit change: new
  `membership` verifying key; genesis regenerated).
  - Identity leaf: `H("earth.leaf", idc, dsc_key, country, activated_at,
    predecessor_at)`; `predecessor_at` is the time of the switch or re-entry
    that made the leaf, 0 for a passport never registered before
    (`Registration.predecessor_at` = 8; genesis `passports_seen` = 17).
  - Membership public inputs: root, scope, nullifier, signal, excluded_dsc,
    excluded_country, max_activation, **max_predecessor** (the circuit checks
    predecessor_at <= max_predecessor). "No bound" is 2^63 - 1.
  - Bounds: caretaker split and handle claim (by a prover holding none):
    max_predecessor < now - lease - 86400, max_activation no bound (a fresh
    registrant acts at once); ballots and removal proposals: max_predecessor
    = opened (or today's start) - 86400, max_activation no bound
    (`QueryBallotInputsResponse.max_predecessor` = 7); ANML claims
    unchanged (max_activation = start of yesterday, max_predecessor no
    bound); moves, renewals and changes: no bound.
  - MsgSetCaretaker: `max_activation` (4) replaced by `max_predecessor` (5).
    caretaker_vote_seconds default **365 days**; renewal is manual.
  - **MsgMoveCaretaker** `{fee, membership (caretaker scope, no bounds),
    new_owner (32-byte caretaker-scope nullifier)}`, sighash [new_owner]:
    hands the live split and its expiry to the new identity; the mover may
    never cast again (ErrCaretakerMovedOut 1126); genesis
    `caretaker_moved_out` (20).
- **Handles replace public referrer addresses** (x/personhood). A
  registered human claims a handle (lowercase a-z, 0-9, -; 3-32 chars; no
  leading/trailing dash) naming their shielded address; the chain is a
  public directory, and payments to a handle are wallet-side (look it up,
  pay its address privately). Referrals are paid as notes.
  - `MsgBindReferrer` is gone (with its consent signature and the
    transparent referral payout). **`MsgBindHandle`** `{fee (1), membership
    (2, scope Scope("handle")), handle (3), address (4, "erthz1..."),
    max_predecessor (6)}`, sighash fields Bytes(handle), owner_pk,
    Bytes(ek_pub). Holding a handle: renew it (lease now +
    `handle_lease_seconds`, param 27, default **365 days**; address may
    change) or change to another (the old one is freed at once). Holding
    none: claim (predecessor bound with the longest lease ever in force,
    genesis `handle_lease_max` = 19). Both empty: release at once. A
    handle held by another nullifier: ErrHandleTaken (1122).
  - **MsgMoveHandle** `{fee, membership (handle scope, no bounds), handle,
    new_owner (32-byte handle-scope nullifier)}`, sighash [Bytes(handle),
    new_owner]: hands the handle (lease unchanged) to the new identity; the
    mover may never claim again (ErrHandleMovedOut 1125); genesis
    `handle_moved_out` (18).
  - Lifecycle: live until `expires_at` (resolves); then for
    `handle_renewal_seconds` (param 26, default 30 days) reserved to its
    owner, not resolving; then free (swept). Nothing renews automatically.
  - **MsgRegister:** `affiliate` (13) and the transparent payout are gone;
    `affiliate_handle` (15), `affiliate_pc` (11), `affiliate_ciphertext`
    (12, 177-byte blind) name a live handle and the referral note the
    registrant's wallet made to its address; the chain mints the referrer's
    half there. Binding affiliate field: 0 for none, else
    H("earth.affiliate", Bytes(handle), affiliate_pc,
    Bytes(affiliate_ciphertext)). Unknown or lapsed handle: ErrNoReferrer
    (1121).
  - Queries `Handle` (`/earth/personhood/v1/handle/{handle}`) and `Handles`
    (`/earth/personhood/v1/handles?start=&limit=`, the whole directory in
    order) return `{handle, address, status (live | renewal | free),
    expires_at, renewal_until}`; `Referrer` is gone. Events `handle_bound`,
    `handle_moved`, `handle_released`, `move_caretaker`. Genesis: `handles`
    (16); `referrer_bindings` (8) removed.

- x/shieldedstaking: **private stake votes no longer spend the note**
  (ORCHARD_DESIGN.md 8.5). One stake note can vote on every
  concurrently open proposal (the decoy-proposal attack on spend-to-vote).
  - The stake nullifier set is now an indexed (sorted) depth-32 Poseidon2
    Merkle tree (zk/indexed); its root and size are recorded at EndBlock
    and every proposal snapshot takes them (`ProposalSnapshot.nf_root`,
    `nf_size`) with the note root.
  - **Wallet format.** MsgStakeVote is `{bundle, proposal_id, validator,
    options, weight, proof (8), vote_nullifier (9)}`; `stake` (7) and the
    response's `position` are gone. `proof` is the new circuit `vote`
    (verifying key `vote` in x/shielded params; genesis regenerated):
    note under the snapshot root, its spend nullifier absent from the
    snapshot nullifier tree (low leaf), `0 < weight <= amount`,
    `vote_nullifier = H("earth.vnf", nk, rho, position, proposal_id)`.
    Sighash fields: proposal_id, Bytes(validator), Bytes(OptionsBytes),
    weight, vote_nullifier. Nothing is spent or re-minted; a second vote of
    the same note on a proposal is refused (code 1119). Wallets rebuild the
    nullifier tree at the snapshot from the first nf_size-1 stake
    nullifiers in insertion order (ORCHARD_DESIGN.md 12.2 has the steps).
  - New query `StakeNullifierTree{start, limit}` (values in insertion order,
    size, current and latest roots); `StakeNullifier` also returns the leaf
    index; `shieldedstaking_stake_nullifier` events carry `index`;
    `shieldedstaking_stake_vote` carries `vote_nullifier`; the snapshot
    event carries `nf_root`, `tree_size`, `nf_size`. **Indexers** must serve
    stake nullifiers in insertion order with their index (failed txs
    included).
  - Gas: a stake vote is fixed at 250,000 + proof_verification_gas +
    note_gas; every other stake msg pays two note writes per nullifier slot
    (+2 x note_gas).
  - Genesis: `stake_nullifiers` are in insertion order; InitGenesis rebuilds
    the tree and checks each snapshot's nf_root. Invariant 7 checks the
    tree's size.
  - Positions unchanged (they never spent to vote).

- Audit round 3 (see AUDIT_HISTORY.md):
  - Node (F1): **the app mempool is always the no-op one.** app.New
    installs it last, overriding app.toml; a mempool.max-txs other than -1
    is logged ("ignoring app.toml mempool.max-txs") and has no effect.
    Before this, a node with max-txs >= 0 failed every private tx in a
    block after its ante wrote (its sender-nonce mempool refused a tx with
    no signer) and forked off the network.
  - Node (F5): PrepareProposal leaves out private txs past the block's
    max_private_actions_per_block (they stay in the mempool for a later
    block) instead of proposing txs certain to fail with ErrBlockCap.
    Not checked in ProcessProposal.
  - x/shieldedstaking (A): reward escrows move only their spendable coins.
    An account someone created at a future validator's escrow address (a
    permanently locked vesting account) no longer blocks compounding or the
    retirement/removal release; the locked coins stay there. A failed
    retirement release moves 24h back in its queue, and pending-release
    retries rotate from a cursor, so entries that keep failing cannot
    starve the bounded per-block/per-epoch budgets.
  - x/shielded, x/shieldedstaking (B/F2): **wallet rule.** An unshield
    (MsgSend remainder) to any module account the app declares is refused
    in the ante ("receiver is the <module> module account"). Private
    staking's account takes pool coins only through x/shielded's
    ReleaseToModule for its own msgs; its invariant 1 stays exact.
  - x/shieldedstaking (D): orphan unbond records are indexed; the epoch end
    no longer walks a validator's whole record list (which grew with every
    unclaimed matured record).
  - x/shieldedstaking (F3): **wallet format rule.** Every vote option
    weight in MsgStakeVote / MsgPositionVote must be the canonical
    LegacyDec string (18 decimals, e.g. "1.000000000000000000",
    "0.500000000000000000"); "1", "0.5", "00.50" are refused. The
    sighash already bound the canonical form; proofs are unchanged.
  - x/shieldedstaking: the module account has no Minter or Burner
    permission. InitGenesis enforces the delegation rule on x/staking's
    genesis (loaded without hooks): only the module and each operator's
    self-bond delegate or unbond; no redelegations.
  - x/shielded (F4): InitGenesis checks every root record against the
    rebuilt note tree at its tree_size and refuses one dated after genesis
    time (a forged or future-dated anchor was accepted).
  - Stake votes on concurrent proposals (C): fixed by the next entry.
  - `earthd gas-check` (M1): a key stored with an empty value (a revoked
    CSCA, any KeySet member) is now read as present. abci_query cannot tell
    it from an absent key, so an empty answer is asked again with
    prove=true and the ics23 proof decides. gas-check's node must serve
    proofs at the pinned height (any IAVL node does); it fails closed
    otherwise. Before this, gas-check approved registrations under a
    revoked CSCA that the chain refuses.
  - x/assembly (M2): a proposal's subjects stay fixed from entering voting
    until x/gov ends its voting. An expedited proposal the chamber ratified
    but stake did not pass (demoted by x/gov to a regular round) keeps
    them; previously each vote re-classified it against the live trust
    store. A voting proposal without fixed subjects is refused (error 1101,
    "has no fixed subjects") instead of being classified per vote.
  - x/personhood (L1, I1, L2): a landed registration's binding is held
    until its proof's current_date + one year (the largest skew governance
    may set) + a day, so raising current_date_max_skew_seconds never
    reopens an A -> B -> A replay (a future-dated proof is covered too); a
    current_date that is not a calendar date (250231) is refused. Genesis
    gains pending_dsc_purges, dsc_rates, country_rates, network_rate (an
    export mid-purge finishes it and keeps the day's caps) and lease_hold.
  - x/personhood, x/assembly (L4/L5): activation bounds no longer follow
    the live identity_root_window_seconds. Proposal and removal ballot
    votes prove max_activation = opened_at - 86400 (MsgProposeRemoval:
    start of today (UTC) - 86400; BallotInputs returns it); caretaker
    splits and referrer bindings max_activation <= now - R - 86400 (R =
    caretaker_vote_seconds). 86400 is the largest root window, so no
    window change, either way, lets an identity and its switched-to
    successor both vote or both hold a lease. A lowered
    caretaker_vote_seconds keeps the old R in the bound until every lease
    cast under it has lapsed (lease_hold). The window now only governs
    root validity. New identities wait a day (was an hour) before they can
    vote.
  - x/personhood (L6): **wallet format change.** MsgBindReferrer binding
    an address needs its owner's consent: referrer_pub_key (field 5,
    33-byte compressed secp256k1 key whose address is `address`) and
    referrer_signature (field 6, 64-byte low-S r||s, cosmos secp256k1 Sign
    = ECDSA over SHA-256) over "earth.referrer.consent.v1" || u8
    len(chain_id) || chain_id || membership.nullifier (32 bytes) || raw
    address bytes. Both empty when clearing. Not sighash fields (proofs
    unchanged). Refused with 1125 ErrNoReferrerConsent. +1000 gas.
  - x/dex (L3): a deposit whose pool-ratio pull would take zero of either
    leg is refused (ErrZeroShares) instead of minting shares against
    nothing; MsgAddLiquidity and private (note) deposits alike.
  - x/personhood (L7): one ANML buyback trade spends at most
    buyback_max_trade_seconds of emission (new param, default 3600; 0 =
    default; between buyback_twap_window_seconds and
    buyback_max_accrual_seconds). A larger backlog (still capped at
    buyback_max_accrual_seconds) is bought over the following windows.
  - Personhood passport and app proof fixtures re-recorded (referral keys,
    one-day activation margin); two x/shielded fixtures added (unshield to
    a module account). Genesis regenerated: networks/genesis.json sha256
    d5385d77cd1df472e049a78467bd896e143bd21009540b55fb578870e96154db.
- Re-audit round 2 (see AUDIT_HISTORY.md):
  - x/personhood (R1): a landed registration's binding (the proof's
    address input) is refused for reuse until registered_at +
    current_date_max_skew_seconds + 1 day (new error 1124
    ErrBindingUsed), by the ante and by `earthd gas-check registration`
    alike; an A -> B -> A replay of a public proof is refused. Genesis
    gains `used_bindings`. registration_sweep_limit must be 0 (default) or
    5..10000; current_date_max_skew_seconds at most a year.
  - x/shielded (R2): CheckTx verifies the msg's own action proofs before
    its bundles' and remembers proofs it saw verify (CheckTx only, never in
    a block): a valid bundle reused next to a junk proof costs one
    verification.
  - zk/ultrahonk (R6): every 32-byte proof element must be below the BN254
    scalar modulus (bb reduced x and x+r alike).
  - Private txs (R7): **wallet format rule.** A private tx is refused unless
    its bytes are exactly TxRaw{body_bytes, auth_info_bytes} with each part
    (and the msg inside the body) the canonical protobuf encoding of what it
    decodes to: fields in field-number order, minimal varints, no default
    (zero) scalars, no unknown or non-critical extension fields; and
    AuthInfo.tip must be unset. CosmJS/protobufjs and gogoproto encoders
    produce this already.
  - x/shieldedstaking (R3): a vesting account cannot operate a validator
    (new error 1118 ErrVestingOperator, at MsgCreateValidator and in
    genesis); compounding is undone if it would move an operator's
    spendable balance.
  - x/shieldedstaking (R4): each private stake vote's deduction is capped at
    what the voted derth is worth now, so stake delegated after the snapshot
    follows the validator's own vote.
  - x/shieldedstaking (R5): a validator's Groundworks index entry goes with
    its last position, and voters re-weigh with the bounded book sweep
    (EpochValidatorLimit a block) instead of one walk at the epoch end.
  - x/shieldedstaking (R8): the epoch end reports a violated unbonding floor
    (epoch_failure stage `unbonding_floor`) and defers an undelegation whose
    max_entries are full (event `shieldedstaking_unbonding_deferred`); its
    records stay PENDING.
  - x/shieldedstaking: genesis exports `pending_releases` and
    `retiring_escrows`; a slash never raises a validator's epoch rate; the
    epoch-end invariant check counts validators (by reward escrow) against
    its bound.
  - x/shieldedstaking: **wallet format rule.** `StakeProof.ciphertexts` has
    exactly two entries, entry i empty iff `commitments[i]` is zero (an
    unused output slot sends an empty ciphertext, not none), and a present
    one is exactly 153 bytes (the wallet stake note: epk || AEAD of 0x03 ||
    asset_id || amount u64 BE || rho || rcm, salt "earth.stake.v1", info
    epk || cm; PRIVACY_FORMATS.md §3). A 177-byte blind stake ciphertext
    there is refused.
  - x/shielded: **wallet format rule.** Every bundle action's output
    ciphertext is exactly 217 bytes (the v1 note ciphertext, salt
    "earth.note.v1"), dummy outputs included: a dummy is encrypted to a
    throwaway key, never left empty or short. Applies to every bundle (sends,
    fee bundles, staking, dex, personhood). Wallets must send:
    bundle outputs 217; StakeProof outputs 153 or empty (zero cm);
    StakeProof.spc_ciphertext 177 when the msg mints a stake note, else
    empty; every chain-minted note's ciphertext 177 (unchanged).
  - Proof fixtures re-recorded with real-length ciphertexts; genesis
    regenerated for the new (empty) genesis fields:
    networks/genesis.json sha256
    c27341544f40c402a31a8b4de0339a057820904c953562beacc35456fd81f5c1.
  - x/assembly: a removal ballot that carried grants no cooldown (a struck
    option leaves no entry; a strike that failed to apply may be balloted
    again at once). A declined ballot still grants 30 days, even one opened
    by the option's own beneficiaries: the ballot is public for seven days
    and anyone can carry it.

- x/shielded (audit M1): **wallet format change.** Every private msg's
  sighash binds the tx's memo, timeout_height and gas_limit:
  `H(TAG_SIGNAL, Bytes(type_url), Bytes(chain_id), K, digests…,
  Bytes(memo), timeout_height, gas_limit, msg fields…)`. A relayer can no
  longer rewrite them. Private txs may not set timeout_timestamp.
- x/shielded, x/personhood, x/shieldedstaking, zk/ultrahonk (audit M1): every
  proof must be exactly 14,656 bytes (bb ignored trailing bytes).
- x/shielded (audit M2, L1-L5): CheckTx verifies proofs one at a time and
  stops at the first failure; the pool cannot mint to, release to or pay
  fees for itself; its module account has no Minter/Burner; action handlers
  declare the denoms they release and the pool refuses any other remainder;
  private tx priority is capped. bb no longer logs failed verifications
  (BB_VERBOSE=1 restores it).
- **Wallet format change: one note-discovery rule.** Every note the chain
  mints carries a required 177-byte amount-blind ciphertext (v2 for pool
  notes; new blind stake ciphertext, salt "earth.stake.v1", version 0x03,
  for stake notes: `StakeProof.spc_ciphertext`, bound last in the stake
  fields). MsgShield's ciphertext is required (gas grant included).
- **Wallet format change: one fee rule.** Private msgs pay their fee from
  their bundles' uerth balance less what they move; staking and dex `fee`
  fields removed; new MsgDelegate.amount, MsgNoteSwap.denom_in/amount_in,
  MsgAddLiquidityShielded.erth_amount; MsgNoteSwap.fee_from_output removed
  (only MsgClaimUnbonding pays from its output; MsgSend keeps its explicit
  fee).

**Operators.** Rate-limit CheckTx per peer on public nodes (sentries,
connection and RPC broadcast limits): a junk private tx now costs one proof
verification, but CheckTx is still free.

- x/shieldedstaking + x/allocation: Groundworks positions are weighed per
  validator. Each validator with live positions is one weighted Groundworks
  voter (`gwpos/` + validator bytes; new `Voter.option_weights`) carrying
  trunc(rate x sum(derth x percent) / 100) per option; the epoch end
  re-weighs validators, not positions. Param `max_positions` removed (proto
  field 3 reserved), `min_position` default 1 ERTH; new `Position.split_epoch`;
  a position's `weight` is filled in by queries, not stored.

- x/assembly: a proposal mixing a revocation (MsgRevokeDsc/MsgRevokeCsca)
  with any other message, or nesting one inside another message (authz
  MsgExec, ...), takes no human votes and fails.
- x/assembly: after a removal ballot on a groundworks option closes, another
  on that option cannot open for 30 days. New genesis field
  `removal_cooldowns`.
- x/personhood: a registration switch to the identity commitment already
  registered is refused (a replayed MsgRegister).
- x/personhood: **wallet format change.** The passport proof's `address`
  input (RegistrationBinding) now also binds the two note ciphertexts:
  `H(TAG_REG, idc, pc_anml, Bytes(ciphertext_anml), pc_erth,
  Bytes(ciphertext_erth), affiliate)`. No circuit or verifying-key change.
- x/personhood: the expiry, caretaker and referrer sweeps each have a
  reserved share of the per-block retirement budget, so a large revoked-signer
  purge cannot starve them.
- Removed the transparent gas grant: `earthd gas-check membership`,
  `Keeper.CheckGasMembership`, `GasScope`/`GasTransparentSignal`.

**Not consensus-affecting (pre-audit cleanup).** Genesis, verifying keys and
wire format unchanged.

- x/shielded: removed the unused fee-from-output path (`FeeFromOutputMsg`,
  `PayFeeFromModule`); no msg had used it since MsgClaimUnbonding was retired.
- Removed unused keeper dependencies, helpers, x/shieldedstaking
  `ErrUnknownRecord` (1105, never returned; the code stays reserved) and the
  `spc` event attribute key.
- Docs: ORCHARD_DESIGN.md is a current-state specification; its history is in
  HISTORY.md and the audit rounds in AUDIT_HISTORY.md (replacing the
  *_PROGRESS.md files). Tests are grouped by feature instead of audit round.

## [v0.9.4]

**Not consensus-affecting. Validators need not upgrade.** Adds `earthd
gas-check`, for the gas-grant backend:

- `earthd gas-check registration` reads a MsgRegister (proto JSON) on stdin
  and says whether the chain would accept it, with its nullifier.
- `earthd gas-check human <address>` says whether an address currently counts
  as a human.

Both run the chain's own personhood and pki checks (the new read-only
`Keeper.CheckRegistration` and `Keeper.CheckHuman`) over a node's state, read
through plain `abci_query /store/...` reads pinned to one height. The proof is
verified by the process running the command, never by the node, so a flood of
junk proofs sent to the backend costs the chain nothing.

## [v0.9.3]

**earth-1 relaunches from a new genesis at 2026-09-29T12:00:00Z.** The same
binary as v0.9.2; only the baked-in genesis changes. Genesis sha256
`acbf85491374558cac98044547ef6f24fa365631ebb254905b3e80489ac46127`.

The previous earth-1 halted at the v0.9.2 plan height (505000): cosmovisor's
pre-upgrade copy of `data/` filled the 20Gi volume, and the crash-looping pod
could not be replaced in place. Its state is gone. Every account, registration
and ballot starts over; the chain id stays `earth-1`.

The new genesis starts where the v0.9.2 handler would have left a chain:

- the v0.9.2 register-circuit verifying keys, so apps built with the widened
  nullifier register from block 1;
- the interchain-account host allowlist the v0.9.2 handler set, instead of `*`;
- the v0.9.2 personhood gas and registration-cap defaults, which `earthd init`
  now produces on its own;
- an x/assembly section, since the module exists at genesis.

The upgrade handlers through v0.9.2 stay registered but never run on this
chain. Launch from the **v0.9.3** image; it is the cosmovisor genesis slot.

## [v0.9.2]

Consensus-breaking. Proposed as gov proposal 7 and reached its plan height
(505000) on the previous earth-1, which halted there — see v0.9.3. It goes through governance as a
`MsgSoftwareUpgrade` named `v0.9.2`. No store changes. The proposal needs two
thirds of the human votes cast as well as stake.

**Wallet apps must ship the recompiled circuits at or before the upgrade
height**, as for v0.9.1: the nullifier changes, and a proof from the old
circuits does not verify against the new keys. The apps' registration gas limit
also rises from 3M to 6M.

**Every registration is retired at the upgrade height.** A registration made
under the old nullifier cannot be matched by a proof under the new one, so the
same passport could otherwise register twice. Each person registers again, once.
ANML already paid stays with its holder. earth-1 had one registration.

The rest of the 2026-09-23 review. The deployment items and automatic upgrade
downloads (kept on, deliberately) are not in this release.

### Fixed

- **The nullifier separates issuing states and covers long document numbers.**
  It was Poseidon2(document number ‖ DOB); document numbers are unique only
  within a state, so two people from different states with the same number and
  birth date shared a nullifier, and the second to register took over the
  first's registration as a wallet switch. Numbers longer than nine characters
  were cut to nine. The preimage is now issuing state ‖ document number ‖ check
  digit ‖ DOB ‖ optional data. New verifying keys for all seven circuits.
- **A CA certificate passed as a Document Signer.** `VerifyDsc` only asked
  whether a trusted key signed the certificate, so a country's self-signed CSCA,
  its link certificates or anything it issued with `cA` or `keyCertSign` could
  be a registration's signer. Refused now with `ErrNotDsc`.
- **The per-country cap is keyed by the issuing CSCA.** It read the country from
  the DSC's own subject, which the issuer can set to anything, and skipped the
  cap when it was empty. A missing country now shares one capped bucket, `??`.
- **The network has a daily registration cap.** It was counted and never
  enforced, so every country's allowance added up without bound. New params
  `network_daily_registration_floor` (5000) and
  `network_daily_registration_growth_bps` (30000, three times yesterday).
- **A wallet switch no longer spends the day's allowance**, and is not refused
  on a day the signer or country is at its cap.
- **Proof and certificate verification cost more gas:** 3,000,000 and 300,000,
  from 1,000,000 and 100,000. Set by the handler. A block holds at most 33
  proofs, which the validator's CPU share can verify within the block time.
- **The proof verifier refuses non-canonical inputs** (a value of p or more,
  which aliases a smaller one) and a public-input count other than the key's.
- **Parameter validation** bounds `registration_validity_seconds` to 1s–3 years
  and requires the four public-input indexes to differ once a key is set.
- **A refused proposal's deposit follows stake's tally.** The assembly refunded
  every deposit it failed, so a proposal stake vetoed as spam got its deposit
  back whenever humans also said no. It is burned whenever x/gov would burn it.
- **A Groundworks strike cannot halt the chain.** It runs in a cache context;
  if x/allocation errors, the strike is rolled back, `assembly_removal_failed`
  is emitted, and the ballot closes as not carried.
- **A cancelled proposal's ballot closes.** x/gov deletes a proposal cancelled
  in its voting period without a hook, and its assembly ballot stayed open.
- **A delegation change no longer revives a split governance reset.**
- **Importing a genesis no longer mints the gap since its export.** x/earth's
  mint clock and x/allocation's upkeep clocks resume at the genesis time.
- **Exchange genesis validation** rejects two pools for one token, a pool
  paired with the hub, pools disagreeing on the hub denom, and auction bids that
  do not sum to `total_raised`.
- **Re-seeding an empty pool burns its residue** (recorded as `dex_residue`)
  instead of handing it to the depositor.
- **The LP unbondings query reads an address index** instead of every
  withdrawal on the chain. The handler builds the index.

### Changed

- **Interchain accounts** may run an explicit list of messages (bank, staking,
  distribution, gov votes and deposits, IBC transfer, the exchange, capital
  allocation, contract execution) instead of `*`.
- **The node image runs as uid 10001** (`earth`). On the first start the
  entrypoint hands an existing root-owned `$EARTH_HOME` to it, once. The
  Barretenberg headers and msgpack-c are pinned by commit and checked.

## [v0.9.1]

Consensus-breaking. Applied on earth-1 at height 467,500 (governance proposal 6).
It went through governance as a `MsgSoftwareUpgrade` named `v0.9.1`. No store changes. Since v0.9.0 the proposal
needs two thirds of the human votes cast as well as stake.

**Wallet apps must ship the recompiled circuits at or before the upgrade
height.** A proof from the old circuits does not verify against the new keys, so
registration from an un-updated app fails from this height on.

### Fixed

- **A genuine passport could mint unlimited registrations.** The register
  circuits checked that each embedded hash — DG1's in the eContent, the
  eContent's in the signed attributes — fitted inside its fixed-size array, but
  not inside the bytes that were actually hashed. `sha256_var` ignores
  everything past its length, so a prover could keep a real SOD's signed prefix
  and park the hash of an invented DG1 in the unhashed tail: any document
  number, any date of birth, any expiry, a fresh nullifier each time, all under
  a genuine Document Signer's signature. The circuits now bound each hash by the
  hashed length, require it directly after its DER prefix (DG1's DataGroupHash
  entry, the messageDigest attribute), and require a whole 93-byte TD3 DG1 with
  its header. All seven circuits are recompiled; the upgrade handler replaces
  every key in `params.verifying_keys`. Registrations made before this height
  were proved under the unsound circuits — earth-1 had one, its operator's.
- **A lazily-settled allocation option could halt the chain.** `AdvanceIndex`
  rounded the options' share of each interval's emission down and booked the
  fraction to residue, but an option that settles once over many blocks
  collects those fractions. The first settle after enough blocks left the module
  short of what its options were owed, and EndBlock's solvency check halted the
  chain. The share is now rounded up. Unreachable on earth-1 so far only because
  every option on it is INTEGRATED and settles every block; the first ADDRESS
  option with votes would have armed it. The upgrade refuses to run if the
  allocation ledger does not already balance.

  Operators will see residue swept to the community pool fall to zero: below a
  stream weight of 10^18 the rounded-up share is the whole reward. The
  per-option truncation dust stays on the module account as solvency surplus
  instead, bounded by one uerth per block plus one per settle.
- **The dex LP reward share is rounded up too, for the same reason.** Pools
  settle lazily, so a pool could collect more than `DistributeLPRewards` had
  booked, and `PendingLpRewards` went negative — reserves backed by ERTH that
  was never paid in, about 1 uerth a block, which the solvency check could not
  see because pools plus pending was unchanged.
- **The dex volume index no longer grows without bound.** It compounds by 14/13
  a day. Months into trading, the LP total outgrew one block's reward times the
  index precision and LP rewards stopped being paid; a few years in, a multiply
  passed 256 bits and panicked, halting the chain through the ANML buyback's
  quote. EndBlock now divides the index and every traded pool's volume by a
  million once the index has grown a millionfold — about every six months —
  settling each pool first. Shares are ratios, so nobody's share moves. It runs
  in a cache branch: a failure emits `volume_index_rebase_failed` and retries
  next block rather than halting. Watch for `volume_index_rebased`.
- **Registrations under a revoked Document Signer stop voting at once, and
  retired registrations' votes are taken back.** `LiveNullifier` and the
  allocation `Weight` checked expiry but not revocation, so a revoked signer's
  registrations could keep voting until the bounded purge reached them. And
  votes already cast were never taken back when a registration expired, was
  purged or was revoked. x/personhood now tells the assembly whenever it retires
  a registration (not on a wallet switch, which keeps the nullifier), and the
  assembly removes that nullifier's votes from every open ballot and its tally.
  A new index, `voted_ballots`, finds them without walking any ballot; the
  upgrade moves votes already cast into it. Retirement is already capped per
  block, so this work comes in bounded pieces.
- **Closing a ballot no longer grows with turnout.** It used to delete every
  vote on the ballot in the block it closed. Each round of voting is now its own
  ballot — a proposal's round, the longer round after an expedited demotion, and
  each removal ballot — so a closed round can never be read as part of the next.
  Closing is O(1): the tally is read and dropped, and the votes are cleared from
  EndBlock afterwards, at most 1,000 per block. The upgrade moves any votes in
  the v0.9.0 layout onto ballots. Queries and genesis are unchanged in shape.
- **A Document Signer's registrations do not vote on its revocation.** A
  proposal carrying `MsgRevokeDsc` refuses a vote from any registration made
  under a signer it revokes (`ErrVoterIsSubject`), so they never reach the
  tally. Without this a compromised signer that had
  registered enough people could vote down its own revocation, and no stake
  could override the chamber. Everyone else votes on it as normal.

### Changed

- **Dex genesis carries LP reward state.** `pending_lp_rewards`,
  `volume_index`, `volume_index_day` and `pool_stale_due` are new genesis
  fields, and export settles every pool into its reserve first, because import
  restarts the LP reward index. An export/import used to halt the first block
  after it, holding ERTH the module could not account for. No effect on a
  running chain; it matters to any relaunch from an export.

## [v0.9.0]

Consensus-breaking, and **shipped**: tagged 2026-09-18, proposed as proposal 5,
and applied on earth-1 at height 391277 under the plan name `v0.9.0`. It adds a
module store, so the binary had to be in place at the plan height.

**Read the assembly item before voting on anything else.** After this height a
governance proposal that no human votes on cannot pass, whatever stake is behind
it. Make sure you can cast an assembly vote first.

### Added

- **Governance is bicameral: `x/assembly`.** Every `x/gov` proposal now also
  needs **two thirds of the human votes cast on it** — one live proof-of-personhood
  registration is one vote — and a proposal that does not get them is failed
  before x/gov tallies it. There are no capital-only proposals any more. Binary
  upgrades, `params.verifying_keys`, the `x/pki` trust anchors, allocation
  options: stake can still originate all of them and can no longer carry any of
  them alone.

  The persons axis had no organ at all before this. `readme.md` has said so
  plainly — stake defined who counted as a person and owned the upgrades where
  the emission constants live — and this is the answer to the first half of it.
  Note what it is not: the chamber cannot originate anything, cannot spend, and
  cannot change a parameter. It agrees or it refuses. A body that can only say no
  cannot direct money to itself, which is what makes giving it reach over the
  whole of governance affordable.

  **There is no quorum and no minimum turnout, deliberately.** Two consequences
  follow and both are accepted rather than overlooked. A proposal nobody votes on
  **fails**, including an upgrade fixing a live bug — apathy freezes governance
  instead of waving things through, and the fallback is what it has always been
  for a stuck chain: operators running a binary they choose. And a single YES
  vote is one of one, which clears two thirds, so while the registry is small the
  chamber's decisions rest on whoever shows up.

  The chamber's rules are compile-time constants in
  `x/assembly/types/keys.go`. There is no `Params` and no `MsgUpdateParams`
  anywhere in the module, which is the point: if x/gov could set the threshold
  the assembly checks it with, it could set it out of reach and the check would
  be decorative. Moving it means shipping a binary every validator chooses to
  run — the same protection the emission split has.

  Mechanically it does not fork x/gov. `x/assembly`'s EndBlocker runs
  immediately **before** x/gov's and removes a refused proposal from the active
  queue, so x/gov's own tally only ever sees proposals that cleared both houses.
  A refused proposal is recorded with x/gov's stake tally intact, so both
  houses' verdicts stay visible, and its deposit is refunded — losing a vote you
  entered correctly is not the thing deposit burning is for.

- **The expedited track asks the assembly for three quarters, and a refusal
  there demotes rather than kills.** x/gov's fast track buys a one-day voting
  period instead of seven and pays for it in agreement — `expedited_threshold`
  is 0.75 against 0.667 — so the chamber asks the same of it rather than less.

  An expedited proposal the chamber declines is converted to a regular one with
  a full voting period ahead of it, which is exactly what x/gov does when its own
  expedited tally falls short. Its deposit rides along untouched, and the
  chamber's one-day tally is discarded so the longer round is counted from zero
  under the ordinary two-thirds bar. Declining on the fast track can mean "not in
  one day" as readily as "never", and nobody who refused it has to return: silence
  fails the second round too.

  **Operators: this lengthens the emergency path.** A compromised Document
  Signer is revoked on the expedited track precisely because it is fast, and
  that revocation now needs three quarters of the human votes cast within
  twenty-four hours. Falling short does not kill it, but it does mean seven more
  days before it can pass. The trust-store runbook should be read with that in
  mind.

- **The assembly alone can remove a groundworks option.** Its one affirmative
  power, by `MsgProposeRemoval` and a seven-day ballot at the same two thirds.
  Stake gets no say: a groundworks option is paid by a stake-weighted vote, so
  letting stake veto a removal would leave humans able to object to a capture and
  unable to end one.

  A removed option is marked rather than deleted. The idle sweep in
  `x/allocation/keeper/prune.go` can delete outright only because it touches
  nothing that carries weight; a struck option has weight *and* voters naming it
  in splits that `resyncVoter` replays on every stake change, without a
  transaction. So the strike zeroes the weight, burns what the option had
  accrued — those coins were minted as it accrued — and leaves the record, which
  is what keeps a replay from erroring out of a staking hook. The now-idle record
  is collected by the sweep that already exists.

### Changed

- **Adding a groundworks ADDRESS option needs governance.** Listing one was
  permissionless in both streams, guarded only by the burned
  `address_option_fee`. That is safe on the caretaker stream and not on the
  groundworks one, because the two weight votes differently. Groundworks weight
  is bonded stake, so an option payable to whoever listed it makes self-voting
  the dominant strategy — point your own weight at your own option and you keep
  everything it draws, against a diffuse share of anything shared. The
  equilibrium is every staker listing their own address: the fund pays out pro
  rata to stake, a second staking yield that builds none of the infrastructure
  it exists for. Nothing already in the design pushes back on it. Revoking votes
  does not, because a self-voter is funded entirely by their own weight and
  other voters leaving raises their share; the fee does not, because it is
  priced against volume and one burn against a perpetual pro-rata claim pays for
  itself.

  `MsgAddAddressOption` on `STREAM_ID_GROUNDWORKS` now requires the module
  authority as `submitter`, and charges no fee — a proposal deposit is that
  path's brake, and burning from the authority account would destroy protocol
  funds rather than a submitter's. The caretaker stream is unchanged and stays
  open to anyone for the fee: one human, one vote means the same move only
  splits the fund equally among registered humans, which is a dividend rather
  than a capture, and personhood caps it at one share each.

  Existing options are untouched; only new listings are affected. Note the
  interaction left standing for now: `MsgResetAllocations` zeroes every
  groundworks option's weight, which schedules the whole slate for pruning at
  `OptionIdleGrace`, and re-listing what gets swept is now a governance cycle
  rather than a fee.

## [v0.8.0]

Consensus-breaking, and **shipped**: tagged 2026-09-01 and registered in
`app/upgrades.go` as the plan name `v0.8.0`.

### Changed

- **The caretaker slate is not governance's to retire.** `MsgResetAllocations`
  now rejects `STREAM_ID_CARETAKER` outright rather than gating it on the
  authority. Stake-weighted `x/gov` is the capital axis, and a reset confiscates
  nothing — accrued ERTH stays with its options and humans can vote again — but a
  stream with no votes accrues to nothing, so the power to fire it repeatedly was
  a mute button on the fund, and registered humans held no matching lever over
  the groundworks slate. The caretaker slate is now redirected the way it was
  meant to be: by humans voting.

  What this gives up is the sybil backstop. A bad verifying key, a compromised
  DSC or a circuit flaw is a sybil break, and a caretaker slate captured by
  counterfeit humans can no longer be cleared by proposal — recovery becomes a
  binary upgrade. That is deliberate: a reset is a live weapon pointed at the
  persons axis every day, while a sybil break is a contingency.

  No state migration and no store changes; the difference is entirely in what
  the handler accepts. Existing votes, epochs and accrued balances are untouched,
  and a caretaker reset that already happened stays happened.

## [v0.7.0]

Consensus-breaking, and **shipped**: tagged 2026-08-29 and registered in
`app/upgrades.go` as the plan name `v0.7.0`. It went through governance as a
single `MsgSoftwareUpgrade` at a height after v0.6.0's, and had to land **before
the genesis liquidity auction opened** — see the LP-share item below for why that
ordering was the safety property.

**This release absorbs v0.6.1.** That tag was built but never proposed: no
`MsgSoftwareUpgrade` named `v0.6.1` ever existed on `earth-1`, so nothing has
ever halted on that name and no node has to replay it. Its four changes are
listed here and its upgrade entry is gone. **Do not propose `v0.6.1`** — this
binary has no handler for it and would halt at the plan height. The v0.6.1 tag
and its release have been withdrawn to stop anyone doing so by mistake.

**Registration needs the updated app.** This is the one change with a
dependency outside this repository — see *Document Signer identity* below.

### Fixed

- **An LP share denom could be a pool's spoke asset, and anyone could use that
  to halt the chain.** `MsgCreatePool` checked that one side was ERTH and
  nothing about the other, so `dexlp/N` was accepted. The dex's solvency check
  cannot survive one: `checkPoolTokenSolvency` compares a pool's spoke reserve
  against the module's entire balance of that denom, which is exact only because
  one pool per token means nothing else claims it. LP shares break that, because
  the module also holds them as the protocol's own position and as escrow
  against withdrawals in flight. A pool claiming one as its reserve turns every
  other such coin into a surplus the module cannot account for, and a surplus
  out of the EndBlocker halts the chain by design. Acquiring a dust amount of
  any pool's shares is permissionless, so this was a halt available to anybody
  from an ordinary transaction, and a CosmWasm contract could send it too.

  Refused now at all four places a pool can come into being: `CreatePool`, the
  auction bid denom, genesis validation, and `SetPool` — the single writer every
  pool write passes through, so a path added later cannot reintroduce it.

- **Pruning an abandoned allocation option halted the chain.** `SweepPrunableOptions`
  burns a dead option's forfeited balance — the coins are real, because emission
  is minted as it accrues — but removed the option record directly rather than
  through `setOption`, so the module's balance fell while `SummedAccrued` kept
  counting it. `CheckSolvency` then read the module as short and
  `AssertHotInvariants` turned that into a halt **in the same block**: the sweep
  runs in BeginBlock and the assertion in EndBlock. The burn had been added to
  protect solvency and was the thing that broke it.

  Arming it needed no privileges — add an ADDRESS option, vote for it until it
  accrues, stop voting, never claim, wait out the 30-day `OptionIdleGrace` — but
  the likelier route was an accident: the first time a real option was abandoned
  with an unclaimed balance.

  The three-line fix is not the important half. `AssertInvariants` — the
  assertion the test suite runs after every operation — checked only the two
  weight invariants, while `AssertHotInvariants`, the one the EndBlocker runs,
  checks solvency first. `prune_test` did everything right and passed anyway. It
  now walks the accrued sum and runs `CheckSolvency`, so the existing test
  catches this without a new one, and the next `setOption` bypass cannot land
  the same way.

- **One malformed LP unbonding froze every withdrawal, permanently.**
  `SweepMaturedUnbondings` returned the first payout error, `x/dex/module` sends
  that out of `EndBlock`, and baseapp does not recover EndBlocker errors — so
  every validator stopped at once. It could not clear itself: an entry is removed
  only *after* its payout succeeds, and the queue is ordered by completion time
  with the loop breaking at the first entry not yet due, so the bad entry sat at
  the head failing identically every block with everyone else's withdrawal behind
  it. Only an upgrade could remove it.

  No transaction could create such an entry — `MsgRemoveLiquidity` validates the
  pool, the share amount and the denom. **Genesis import had no validation at
  all**, and `earth-1` relaunched from a fresh genesis on 2026-08-28 while
  upgrades round-trip state through `ExportGenesis`/`InitGenesis`.

  The sweep now drops a bad entry instead of dying on it: it logs, emits
  `lp_unbond_payout_failed` naming the pool, provider and shares, and removes the
  key. Each payout runs in a cache branch, so a payout that fails halfway leaves
  nothing behind. Genesis validation refuses the three shapes up front — a pool
  that does not exist, shares that are nil or non-positive, a denom that is not
  the pool's — and pool reserves must now be non-nil and positive for the same
  reason: a nil `math.Int` survives import and **panics** on first use, which
  kills the node with no diagnosis at all.

  `AssertHotInvariants` is untouched. Its halt is deliberate and is a different
  thing: it fires over a module already known to be wrong.

- **Solvency went blind after a genesis round-trip.** Neither `SummedAccrued`
  nor `Residue` survived an export/import. `SummedAccrued` is derived, and
  `InitGenesis` rebuilt `SummedWeight` from the imported options but not it; so
  after any relaunch it read zero while the options carried real balances and the
  module account held the coins backing them. `owed` collapsed to roughly
  `residue`, and because `SolvencyReport.Broken()` tests only `Short` the gap
  read as a tolerated surplus — the check stayed green while unable to see a
  shortfall up to the size of the whole un-imported balance. `Residue` is not
  derivable and is now a genesis field in both directions; without it the coins
  behind it sat on the module account with `SweepResidue` having nothing to move,
  stranding emission earmarked for the community pool.

  `InitGenesis` also now refuses a genesis the module account cannot back,
  reusing `CheckSolvency` so an import is held to exactly the rule the EndBlocker
  enforces afterwards.

### Changed

- **`MsgAddLiquidity` takes a `min_shares`.** A deposit is priced at whatever
  ratio the pool holds when it executes, so a trade landing between signing and
  execution changes what it mints — the ordinary sandwich, which `MsgSwap` has
  always had `min_amount_out` to refuse and add-liquidity had nothing against.
  Wire-compatible: the field is new, and a client that sends nothing gets the
  old behaviour exactly, so transactions already in flight are unaffected.
- **A wallet switch keeps the person's ANML clock.** Moving a live registration
  to a new wallet rebuilt it from scratch, which set the claim clock to today's
  midnight — the value a brand new registration gets so its first claim opens
  tomorrow. Applied to someone who had been registered for months and had not
  yet claimed today, that quietly took the day. It cannot be turned into a
  second claim: `ClaimAnml` compares day numbers, so a carried clock already on
  today still reads as claimed.
- **Certificate verification has a stated worst case.** `VerifyDsc` was charged
  one flat `dsc_verification_gas` for work whose size the submitter chose: how
  large a public key the certificate declares, and how many trust-store
  certificates its issuer names. Both are now bounded — 2048 bytes and 32
  candidates. Neither is near real data (the bundled master list tops out at 768
  bytes and 13 certificates under one DN), and `TestTrustStoreFitsTheCaps` fails
  the build if a future trust-store update comes within 2x of either. The
  candidate cap truncates rather than refusing, and the AKI lookup runs first,
  so a Document Signer that names its issuer finds it well before the ceiling.
- **Document Signer identity now includes the curve.** The DSC commitment —
  `Poseidon2` over a signer's canonical public key, which is what
  `Registration.dsc_key` stores, what `RegCountByDsc` rate-limits on and what
  `RevokedDscCommitments` is keyed by — carried no indication of which curve
  produced those bytes. Length was never the gap: the sponge absorbs the input
  length into its capacity slot, so RSA collides with nothing. Same-width curves
  were: **P-256 against brainpoolP256r1** (64 bytes each) and **P-384 against
  brainpoolP384r1** (96 each). Two such certificates with coinciding coordinates
  would have shared one on-chain identity, so revoking one signer would have
  retired the other's registrations.

  Not exploitable — it needs two certificates a trusted CSCA actually signed
  whose coordinates coincide — and shipped now because the cost only grows: the
  chain never stores the public key behind a commitment, so every registration
  made before the fix is one a format change strands. There was **one** on
  `earth-1`, and it is migrated rather than stranded (below).

### Operators

- **Propose `v0.7.0`, never `v0.6.1`.** This binary carries handlers for
  `v0.6.0` and `v0.7.0` only. A plan named `v0.6.1` would halt the chain at its
  height with no binary able to continue.
- **Not exploitable on `earth-1` before the auction settles.** `MsgCreatePool`
  is refused while the genesis liquidity auction is unsettled, and at the time
  of writing it is `AUCTION_STATUS_PENDING`. That is what makes this a scheduled
  upgrade rather than an emergency — but it is also a deadline: settling the
  auction opens pool creation, and the window closes the moment it does. Do not
  start the auction until this upgrade has been applied.
- **The verifying keys are swapped by the upgrade handler, not by a separate
  proposal.** New circuits mean new verifying keys, and those live in
  `x/personhood` params — normally a `MsgUpdateParams` vote. That would be
  actively unsafe here: a param change executes when its proposal passes and the
  binary swaps at the plan height, so between the two, registration breaks in
  either ordering — a new-circuit proof verifies against a new key and then fails
  the commitment comparison against the old binary, and an old-circuit proof
  fails the new key outright. Doing it in the handler makes both flip in one
  state transition, and keeps this release a single proposal.
- **The genesis file is untouched, and must stay that way.**
  `networks/genesis.json` still carries the *old* verifying keys, because that is
  what block 0 installs and its sha256 is what `RESET_ON_GENESIS_MISMATCH` is
  keyed to. The new keys are embedded separately, under
  `app/upgrades/v070/verifying-keys/`, and are read only by the handler.
- **Replay and archive nodes are unaffected, but need the binary chain.**
  Cosmovisor runs the pre-upgrade binary for pre-upgrade blocks, so the old
  commitment format and the old keys apply below the upgrade height and the new
  pair above it; the handler's writes are ordinary deterministic state
  transitions and replay identically. The v0.7.0 binary **cannot** replay from
  genesis on its own — it needs v0.5.2 and v0.6.0 in the cosmovisor
  `upgrades/` tree. That has been true since v0.6.0.
- **The one existing registration is migrated, not stranded.** The chain stores
  a commitment and never the key behind it, so an old-format `dsc_key` cannot be
  recomputed from state. The Document Signer's certificate was recovered from the
  `MsgRegister` in block 4833 and is embedded, and the handler rewrites the
  commitment in all six places it is used as an identity: the registration
  record, `RegByDsc`, `RegCountByDsc`, `DscRate`, `PendingDscPurge`, and x/pki's
  `RevokedDscCommitments`. A registration whose `dsc_key` no embedded certificate
  reproduces makes the handler **refuse to run** rather than strand it.
- **Registration requires the updated app.** The seven compiled circuits shipped
  in the app produce the commitment this binary checks against, so a proof from a
  pre-v0.7.0 app is rejected after the upgrade height. Have the app update out
  before proposing.
- **The handler checks five state preconditions before it writes anything.** All
  fail loudly at the scheduled height, with an operator present, rather than
  producing a quiet failure later. None is expected to fire.
  - A pool already holding an LP denom, which would make this upgrade cause the
    halt it prevents, because `SetPool` now rejects every write to one and is
    reached from EndBlocker paths.
  - A CSCA already in the trust store whose key exceeds the new 2048-byte
    ceiling, which would stop being a trust anchor *silently* —
    `issuerCandidates` skips a certificate it cannot parse, and the first sign
    would be a country's passports failing to register for no visible reason.
  - A malformed LP unbonding already queued, which the new sweep would drop
    unpaid rather than halt on.
  - An allocation ledger that does not already balance, which would halt the
    chain on the first block after the upgrade with the fix apparently to blame.
  - A registration whose `dsc_key` cannot be migrated.
- **No state migration for the module set, and no store changes.** `app_version`
  stays at 1: `chain.json` documents that it does not move on its own, and
  editing it would change the genesis hash.

## [v0.6.0]

Consensus-breaking. Scheduled through governance as upgrade name `v0.6.0`.

### Removed

- **`MsgUnregister`.** It freed the registration's nullifier, and a free
  nullifier is a stranger to `Register` — which pays the registration reward
  and mints 1 ANML for any nullifier that is not already live. So
  unregister-then-register was a loop: one passport, once per block, for a full
  payout each time. It was not hypothetical. On `earth-1` it ran in six blocks
  (4827 unregister, 4833 register) for a second payout of 31,534 ERTH, and
  63,070 ERTH drawn in total by one human.

  What was mostly being used for is already covered: registering again from a
  different wallet moves a live registration and deliberately pays nothing. The
  circuit binds the registrant's address, which is what lets that branch exist
  without being registration theft. What is genuinely given up is leaving the
  registry outright, which now happens only on expiry or a Document Signer
  revocation. That is a knowing trade, not an oversight.

### Operators

- **The message type is still registered and still decodes.** Only the handler
  changed, and it now always returns `ErrUnregisterRemoved`. Deleting the type
  would have unregistered it from the interface registry, and the historical
  `MsgUnregister` in block 4827 would stop decoding — every transaction query
  touching that block would fail rather than show what happened.
- **No state migration and no store changes.** The change is entirely in what
  the handler accepts, and registrations retired before the upgrade height stay
  retired. `app_version` stays at 1: `chain.json` documents that it does not
  move on its own, and editing it would change the genesis hash that
  `RESET_ON_GENESIS_MISMATCH` is keyed to.
- **Replay is unaffected.** Cosmovisor runs the pre-upgrade binary for
  pre-upgrade blocks, so a node syncing from genesis sees the old behaviour up
  to the upgrade height and the new behaviour after it.

## [v0.5.2] — launch

`earth-1` launched from this release. The v0.5.0 and v0.5.1 tags were released
against a chain that ran for 45 minutes and was discarded; this is the genesis
the network actually runs.

### Operators

- **A node can state sync from the container image.** The image could produce
  snapshots and not consume them, so anyone using it to run a second node had to
  replay from genesis. Set `STATESYNC_RPC_SERVERS`, `STATESYNC_TRUST_HEIGHT` and
  `STATESYNC_TRUST_HASH`.
- Cosmovisor's binary slot now follows that choice. Replaying wants the launch
  binary in `cosmovisor/genesis/bin` so it walks each upgrade; state syncing
  lands past every upgrade and must start on the current binary instead. Putting
  the image in the genesis slot unconditionally was right for the first and
  silently wrong for the second.

### Fixed

- `chain.json` claimed `app_version` "moves with every coordinated upgrade, and
  x/upgrade uses it to refuse a binary that is not the one the network agreed to
  run". None of that is true in SDK v0.53: nothing bumps it, and x/upgrade never
  reads it. It documented a protection the code does not have.
- Dropped the redundant deprecated `module.AppModule` assertion from all five
  modules; each already asserts `appmodule.AppModule`, so the migration was done
  and the old line was a leftover.

## [v0.5.1]

Dead code and stale documentation only; nothing here is consensus state. It is
the chain's first governance upgrade, kept deliberately trivial because
`app/upgrades.go` had never run against live validators — only in
`scripts/rehearse-cosmovisor.sh`. Operators need do nothing: cosmovisor
downloads and swaps it at the plan height.

## [v0.5.0] — launch

`earth-1` launched from this release at 2026-08-28T06:30:00Z, genesis sha256
`3701aa69c304f45bbede5bb9eef3b7770d57dd7c03f39caa8c1d7b8a1ea4f792`.

This section covers everything since v0.2.1 rather than one tag's worth. That is
not tidiness lost: the v0.3.x and v0.4.x tags were released against a chain that
no longer exists, and the chain running today started at v0.5.0 with all of this
in it. For an operator deciding what to run, this is the first release there is.

### Consensus

- **The chain publishes its issuance rate again.** Overriding x/mint's mint
  function left x/mint's own `Minter` at its zero value, so
  `/cosmos/mint/v1beta1/inflation` and `.../annual_provisions` both answered
  `0.000000000000000000`. Nothing in this project reads them — both clients
  compute from the fixed per-second rate — but wallets, explorers and
  aggregators derive staking yield and monetary policy from exactly those two
  fields, and a chain issuing 126,144,000 ERTH a year was telling every one of
  them it issued nothing.

  What is published is **gross** issuance: 126,144,000 ERTH a year, and that over
  total supply, which is ~5% at genesis. That is what the fields mean —
  `AnnualProvisions = TotalSupply * Inflation` is the SDK's own identity and it
  describes minting, not the change in supply. It is not the whole picture: this
  chain also burns, currently faster than it mints, so supply is falling while
  this number is positive. There is nowhere in x/mint to say so. Its eight fields
  are all about positive issuance and `ValidateMinter` rejects a negative
  inflation, so a net figure is not representable even in principle. Net lives at
  `/earth/earth/v1/burns`, which reports both sides.

  **Expect this number to rise before it falls.** The decay described in
  x/earth/types/keys.go assumes a growing supply. While the protocol-owned
  liquidity retires, supply shrinks, so a fixed numerator over a smaller
  denominator climbs — for about five years, and only then begins the decay.

- **The chain now counts what it burns.** Five mechanisms destroy supply — the
  gas split, the dex swap fee, protocol-owned liquidity retiring, the ANML
  buyback, and forfeited allocation rewards — and until now none of them left a
  figure anyone could read. Three run in EndBlock, so no transaction search can
  find them, and x/bank records only the supply that remains, never what left
  it. The totals were therefore unrecoverable after the fact: counted as they
  happen or not at all.

  x/earth now keeps a running total per `(source, denom)`, queryable at
  `/earth/earth/v1/burns` and `earthd query earth burns`. LP shares burned when
  liquidity is withdrawn are deliberately excluded — they are a claim on a pool,
  not supply, and counting them would inflate the figure with bookkeeping.

  **This is consensus-affecting**: it adds state writes on every burn path. A
  node running an older binary will not agree with one running this.

  Counters start at zero from the height this takes effect. A chain upgrading in
  place therefore reports only what it burned after the upgrade, which is why
  this release is paired with a genesis reset rather than an in-place swap.

- **The earth module's genesis is now lossless.** `last_mint_time` was written by
  the keeper but never exported, so an export/import silently reset the emission
  clock and skipped one block's issuance. It is exported now, alongside the new
  burn counters — the one piece of state that cannot be reconstructed from
  anything else the chain keeps.

### Testing

- **Burn sites are pinned.** The counters are a parallel record — x/bank moves
  the supply and each call site is separately responsible for saying so — and
  nothing at runtime notices when the second half is forgotten. The burn still
  happens; the total is quietly short, and no later observation recovers it.
  `TestEveryBurnSiteIsAccountedFor` walks the modules for calls to `BurnCoins`
  and fails until each is listed with the source it counts under, or a reason it
  must not be. The test bank stubs in x/dex and x/allocation now assert on every
  fixture that what the bank destroyed is what the counters saw, which turns
  every existing test over a burn path into a completeness check.

### Operators

- **Downloaded releases could not start.** Every published tarball up to and
  including `v0.4.5` contained `earthd` and `LICENSE` and nothing else, while
  the binary needs `libwasmvm` as a shared library. wasmvm's cgo directive bakes
  in `-Wl,-rpath,<the builder's Go module cache>`, which was the only rpath in
  the binary, so running it anywhere but the machine that built it gave

      libwasmvm.x86_64.so: cannot open shared object file

  The container image was never affected — it copies the library into
  `/usr/local/lib` and runs `ldconfig` — which is why this survived five
  releases. Anyone following `docs/run-a-node/join.md` hit it immediately.

  **The tarball layout has changed.** It now holds `bin/earthd` and a `lib/`
  with the matching `libwasmvm`, `libc++`, `libc++abi` and `libunwind`, and the
  binary carries `-Wl,-rpath,$ORIGIN/../lib` so it finds them wherever it is
  unpacked. `libc`, `libm` and the loader still come from the host, deliberately.

      sudo install -m755 <dir>/bin/earthd /usr/local/bin/
      sudo install -m644 <dir>/lib/*      /usr/local/lib/

  Both lines are required. `earthd` and `libwasmvm` are version-locked: never
  pair one release's binary with another release's library.

- **Cosmovisor is available, and optional.** It ships in the container image and
  is off unless `USE_COSMOVISOR=true`. Nothing changes for a node that does not
  set it. `docs/run-a-node/upgrades.md` documents the manual path and the
  cosmovisor path side by side.

  The release archive is rooted at `bin/` with no wrapper directory so that
  cosmovisor's auto-download works: it accepts `/<daemon>` or `/bin/<daemon>`
  and nothing else, and a wrapper directory fails at the upgrade height with the
  chain already halted.

- **The first real upgrade handler.** `app/upgrades.go` carries an entry for
  `v0.4.0`. Its migration does nothing, deliberately: this release needs no
  migration, which makes it the safest possible first exercise of a path that
  had never run outside `scripts/rehearse-upgrade.sh`.

  So there are two ways to take this release, and both are correct:

      in place       swap the binary. Nothing halts; the handler never runs.
      by governance  submit MsgSoftwareUpgrade for "v0.4.0" at a height. The
                     chain halts there, you swap the binary, it resumes.

  The second is a rehearsal with nothing at stake. Practise it here rather than
  on the release that finally does need a migration.

- `scripts/rehearse-upgrade.sh` no longer assumes `Upgrades` is empty. It
  needed a plan name the binary has no handler for, not an empty list — the old
  check would have retired the script permanently at the first real upgrade,
  which is precisely when rehearsing starts to matter.

### Breaking — clients, NOT consensus

- **Pool queries return real volume instead of an internal weight.** `GetPool`
  and `ListPool` now answer with `PoolView`, whose `volume_erth` is 14-day
  weighted swap volume in actual uerth. The stored `Pool.volume` is renamed
  `volume_weight` and no longer leaves the module; `last_volume_day` becomes
  `last_traded_day`.

  The old field was swap volume multiplied by a chain-wide index that grows
  7.7% a day forever, and the proto documented it as "decaying ERTH-denominated
  swap volume (~7-day half-life window)" — a description of the mechanism it
  replaced. The wallet implemented that description faithfully and its LP fee
  APR inflated with the index: right by accident on day one, 18x out within a
  month, 1,580x within three. Publishing a figure that has to be de-scaled
  before it means anything is what made that possible, so it is not published.

  The de-scaling happens at query time from state that already exists. Nothing
  new is stored.

  **This is not consensus-affecting and needs no coordinated upgrade.** The
  field numbers and types are unchanged, so stored bytes are identical and a
  new binary reads existing state exactly as the old one did; only the query
  layer moved. Nodes can be replaced in place, and a mixed network still agrees.

  Clients reading `volume` must read `volume_erth` and must NOT decay it — the
  chain has already done the weighting. `networks/genesis.json` changes, because
  its JSON carries field names; that matters only to a chain launching fresh
  from it.

### Breaking — consensus

- **A malformed allocation vote could halt the chain.** `MsgSetAllocations`
  checked only that a voter's percentages summed to 100, over a `uint64` that
  wraps. Two shares near 2^63 sum to exactly 100 modulo 2^64, passing that
  check, and then sign-flip to a negative weight when applied — which the next
  block's stream-weight invariant catches, halting every validator by design.
  Any account with voting weight (a single small delegation, or one
  registration) could trigger it in one transaction.

  Each share is now rejected on its own if it exceeds 100, in both the live
  message path and genesis validation, so neither the sum nor the later cast can
  overflow. Consensus-affecting: a patched node rejects a transaction an
  unpatched one accepts, so the whole set must upgrade together. No state
  migration — the guard is purely on inbound validation.

- **ERTH and ANML now carry denom metadata.** Wallets and explorers read
  `bank.denom_metadata` to know that 1,500,000 `uerth` should be shown as
  `1.5 ERTH`. Without it Keplr and every explorer fall back to the raw
  micro-denom, which is what the running devnet does today —
  `/cosmos/bank/v1beta1/denoms_metadata` returns an empty list on it.

  `uerth` displays as `erth` (symbol ERTH, exponent 6) and `uanml` as `anml`
  (symbol ANML, exponent 6), matching the micro-unit convention used everywhere
  else in the repo.

  Genesis state, not a parameter: there is no `MsgSetDenomMetadata`, so a chain
  that launches without this can only get it through a governance proposal
  executing as the bank authority, or a relaunch. `validate-genesis` will not
  catch its absence either — the SDK validates metadata that is present and an
  empty list is perfectly valid — so `networks/genesis_test.go` asserts it
  instead.

  `dexlp/*` is deliberately excluded: LP share denoms are minted per pool at
  runtime, so there is no fixed set to declare at genesis.

  This also fixes `/cosmos.bank.v1beta1.Query/DenomMetadata` for contracts. The
  path was already on the CosmWasm query allowlist but returned NOT_FOUND,
  because there was no metadata behind it.

## [v0.2.1]

### Fixed

- **A devnet whose first boot was interrupted no longer crash-loops forever.**
  The `DEV_INIT` path in `docker/entrypoint.sh` writes
  `config/genesis.json` early and only collects the gentx several commands
  later. Anything that interrupted it in between left a genesis with an empty
  `gen_txs` on the volume, and because the resume path only checks that a
  genesis *exists*, every later start resumed that file and died with

      error during handshake: error on replay: validator set is empty after
      InitGenesis

  forever, with no way out but destroying the volume. The v0.2.0 lease hit
  exactly this: the pod reported available, never ready, and the Akash Console
  API exposes no logs to say why.

  The entrypoint now writes a `.devinit-complete` marker only after
  `collect-gentxs` succeeds, and treats a devnet genesis with no gentx as
  broken-by-construction — rebuilding it rather than resuming a chain that
  cannot produce a block. A healthy chain from an older image is adopted
  unchanged. `collect-gentxs` also no longer discards stderr, which is what made
  the original failure invisible.

- **The image now proves `earthd` runs before it ships.** A `RUN earthd --help`
  in the runtime stage forces the dynamic loader to resolve every NEEDED entry,
  libwasmvm included, so a binary that cannot start is a red build rather than a
  container that exits instantly somewhere you cannot read logs.

## [v0.2.0]

### Breaking — consensus

- **Permissionless CosmWasm.** The chain runs `x/wasm` (wasmd v0.61.14). Anyone
  may upload contract code and anyone may instantiate it, paying only gas —
  `code_upload_access` and `instantiate_default_permission` are both `Everybody`
  in genesis. Both are governance parameters, so they can be tightened later
  without a binary upgrade.
  - **Contracts can reach this chain's modules.** Messages go out as
    `CosmosMsg::Any` through the normal `MsgServiceRouter`, so a contract can do
    anything its sender could do and no more. Reads go through an allowlist in
    `app/wasm.go` — `/earth.personhood.v1.Query/Registration` above all, which
    is what lets a contract ask whether an address is a live verified human.
    Every path on that list is a promise not to change the response's wire
    shape, so it is deliberately short; paginated list queries are excluded.
  - **Contracts get IBC.** They can own ports (v1 and v2), and the callbacks
    middleware now wraps transfer and the ICA controller, so a packet can name a
    contract to invoke on receipt, acknowledgement or timeout.
  - **New store key `wasm` and a new module account.** The account holds the
    `Burner` permission only and is blocked from receiving transfers. Contract
    addresses are ordinary accounts and are not blocked.
  - **Operators: `libwasmvm` is now a runtime dependency.** The shared library
    ships in the container image; anyone building `earthd` themselves gets it
    from the Go module cache via the linker's rpath. A binary copied off the
    build host without it dies at startup with `libwasmvm.so: cannot open
    shared object file`.
  - **Operators: `app.toml` gains a `[wasm]` section** — `query_gas_limit`,
    `memory_cache_size`, `simulation_gas_limit`. Node-local, not consensus;
    nodes may differ without forking. A config generated by an earlier version
    has no such section and falls back to the compiled-in defaults.
- **The ante handler is now built in `app/ante.go`** rather than taken from
  x/auth/tx/config's default. Three wasm decorators are required for contracts
  to run at all, and two more were added that this chain should have had:
  - **The circuit breaker now works.** `x/circuit` was wired and its governance
    messages worked, but nothing consulted the tripped-message set, so
    "disable this message type" silently did nothing. The one lever for halting
    a misbehaving module during an incident was connected to a switch that was
    not attached.
  - **Redundant IBC relays are rejected**, so a relayer that loses a race pays
    no fee for the duplicate. Without it, relaying against earth costs more than
    relaying against anyone else.
- **`x/pki` can revoke a CSCA.** `MsgRevokeCsca` (governance) withdraws trust
  from a Country Signing CA, so no Document Signer chaining to it verifies from
  then on. `AddCsca` used to be a one-way door: a state in the trust store can
  sign as many Document Signers as it likes and each mints identities the chain
  counts as distinct humans, and the only answer was revoking those signers one
  at a time, after each was already in use.
  - What is revoked is the **signing key**, not the certificate handed in.
    Countries carry several CSCA certificates sharing one key — renewals, link
    certificates — and any of them verifies a child signature, so revoking a
    single certificate would have changed nothing.
  - **Prospective only.** Registrations already made keep claiming; retire them
    with `MsgRevokeDsc`, which carries the purge, or let them lapse within one
    `registration_validity_seconds`.
  - Re-adding the certificate with `MsgAddCsca` clears the revocation. That is
    the only way back — there is no un-revoke message.
  - New genesis field `pki.revoked_cscas`. Restored *after* the CSCA list,
    because replaying a CSCA clears its own revocation.
- **The trust store no longer carries Israel.** The three Israeli CSCAs ICAO does
  not distribute were removed from genesis, leaving the ICAO master list alone:
  539 CSCAs down to 536. Israeli passports cannot register — there is no
  ICAO-distributed Israeli CSCA to fall back on. Governance can add them back
  with `MsgAddCsca`; genesis was the only point at which they could be taken out.
- **`x/dex` checks its own books every block.** The EndBlocker asserts that what
  the module records and what it holds are exactly equal, and halts the node if
  not. Deliberate: a halt is recoverable by upgrade, a silent drain of the
  pre-mine is not.
- **`x/allocation` verifies each stream's weight every block**, with the same
  halting behaviour.
- **Creating a dex pool is refused until the genesis liquidity auction settles.**
  The auction has to be able to claim its bid denom and cannot defend it: the dex
  allows one pool per spoke token, `MsgStartLiquidityAuction` refuses to open when
  that denom already has a pool, and nothing can delete a pool — so a dust pool
  created beforehand would have blocked the auction permanently, and the proposal
  to open it publishes the denom a voting period in advance. The lock is blanket
  rather than denom-specific, needs nothing configured, and lifts itself when
  settlement creates the pool; from then on the ordinary one-pool-per-token guard
  protects that denom. A chain with no auction configured is never locked.
- **Allocation emission is minted when it accrues, not when it is claimed.**
  `x/allocation` issues each stream's `1 ERTH/sec` into its own module account as
  the reward index advances, and every payout — option claims, the LP
  auto-compound, registration rewards, the community pool — is now a transfer out
  of it. Neither `x/dex` nor `x/personhood` mints allocation ERTH any more; the
  only ERTH minted outside `x/allocation` is the ANML buyback's own pillar, which
  is a separate emission and mints what it immediately spends. Reported supply is
  therefore what the chain owes rather than what has been collected, the emission
  rate can be checked against the block clock, and a new O(1) solvency invariant
  compares what the options say they hold against what the module is carrying. A
  stream with no votes mints nothing. Index truncation is swept to the community
  pool, where x/distribution already puts the dust from its per-validator split.
- **LP reward volume is scaled instead of decayed, and dead pools are swept.**
  A pool's volume was aged only when something touched it, while the denominator
  it was measured against kept the un-aged figure — so pools were credited less
  than the stream released on their behalf, and 9-11% of the LP emission went to
  nobody. Volume is now recorded multiplied by a global index that grows 14/13 a
  day (half-life ~9.4 days, twice the LP unbonding period), which produces the
  same weighting with nothing to age. Because scaled volume never reaches zero,
  trading starts a 60-day timer and a capped per-block sweep retires the weight of
  pools that stop trading, so a dead pool neither earns nor dilutes. The depth cap
  keeps its own 7-day window and is unchanged at 2x reserve per day.
- **The pre-mine splits four ways instead of three, and the registration-reward
  pool is pre-funded.** 630,720,000 ERTH each — five years of the whole chain's
  emission — to pool 1's reserve, both auction earmarks, and the human stream's
  option #1, which now starts with real coins on the `x/allocation` account
  (`allocation.registration_reward_seed`). The draw rate moves from basis points
  to parts per million and drops from 10 bps to 100 ppm, so the reward halves
  every 6,931 registrations instead of every 693 — $50 a side for the first
  registrant and their referrer at a $1M clear, still $18 a side at the
  ten-thousandth human. The finer unit exists because the unreferred branch
  halves the rate in integer arithmetic: in whole basis points the smallest
  usable rate was 2, since 1/2 truncated to zero and paid an unreferred
  registrant nothing without erroring. ANML's opening price is unchanged, since
  pool 1's ERTH side and the bidders' earmark move together.
- **The per-country daily registration floor drops from 10,000 to 1,000**, and
  the cap is now checked once, read-only, *before* the SNARK is verified rather
  than only after. A country sitting at its cap previously cost a full 4-6ms
  proof verification per rejected attempt; it now costs a map lookup. The
  authoritative check stays where it was — the country is only trustworthy once
  VerifyDsc has chained the certificate to a CSCA, so the early check runs after
  that and writes nothing, and cannot be used to exhaust anyone's allowance.
- **Protocol-owned liquidity is retired over five years.** The genesis ANML/ERTH
  pool and the liquidity auction's pool were permanent; they now burn down on a
  straight line. ANML/ERTH burns both assets, the auction pool burns only ERTH.
  Each quarter of the pre-mine is five years of the whole chain's emission and
  two of them sit in POL, so retiring them over five years burns 1,261,440,000
  ERTH against the pillars' 630,720,000 — supply falls by 630,720,000 over the
  window and only starts growing once the schedule is spent.
- **The chain's own module accounts refuse ordinary transfers.** Sending to one
  was always a mistake, and it is now rejected rather than absorbed.
- **`x/pki` stores one record per certificate, not per signing key.** The trust
  store held 366 of 539 CSCAs; certificates sharing a key overwrote each other.
- **Genesis export carries the state it used to drop** — every registration and
  its nullifier, every revoked Document Signer, every allocation option and vote.
- **Block gas limit set to 100,000,000.** It was `-1`, meaning none.
- **Governance needs two thirds, not a simple majority.** `threshold` 0.5 → 0.667,
  and `expedited_threshold` 0.667 → 0.75 because the SDK requires the expedited
  bar to be strictly higher. Quorum and veto are unchanged at 33.4%.
- **Downtime tolerance raised to 10,000 blocks / 5%**, from the SDK defaults of
  100 / 50% — about four minutes, which with one validator meant a container
  restart halted the chain.

### Operators

- **The container joins a network instead of creating one.** It installs the
  genesis baked into the image, checks it against the published sha256, and
  starts. The old self-init behaviour is now `DEV_INIT=1`, and it makes a new
  chain every time.
- **`--api.enabled-unsafe-cors` is off by default.** Set `API_UNSAFE_CORS=1` if
  something needs the LCD from a browser. New `RPC_CORS_ORIGINS` takes a scoped
  allowlist for the RPC, which no deployment has ever had.
- **Minimum gas price is `0.005uerth`.** Transactions without `--gas-prices` are
  now rejected.
- **Binaries are published.** Linux amd64 and arm64, with checksums and the
  genesis file, on the releases page. Verified by a full dry run.
- **State-sync snapshots are on by default** — every 1000 blocks, keeping 5.
  The SDK default is off, and a snapshot cannot be made for a height already
  passed, so a chain launched without them leaves everyone who joins later
  replaying from genesis. `SNAPSHOT_INTERVAL=0` disables them, loudly.
- **Dead allocation options are removed.** An ADDRESS option that carries no
  weight for thirty days is deleted, and any ERTH it earned and nobody claimed
  goes with it — not burned, since an option's rewards are only minted when they
  are claimed. Anyone may trigger a claim on such an option and the payout goes
  to its recipient regardless of who sent it, so a live recipient has thirty days
  and a permissionless way to take what is theirs. Governance's INTEGRATED
  options are never touched. Capped at 20 removals a block; a quiet block reads
  one key.
- **The `Options` query is paged.** It returned every option in a stream on a
  route that costs the caller nothing, while the number of options is set by
  whoever pays the fee to add them. One request now returns at most 100. Clients
  that read the whole list must follow `pagination.next_key`.
- **The allocation invariant no longer walks every option.** It ran in the
  EndBlocker and summed each stream by decoding every option in it, while adding
  an option is permissionless — so the per-block cost of every node was
  something an outsider could raise for a one-time fee. The sum is maintained on
  write instead and the check compares two numbers per stream: 4,246 gas at five
  options and 4,246 at five hundred. It still halts on the drift it was added
  for, including the clamped case. The exhaustive walk moved to
  `AssertInvariants`, which tests run after every operation and operators can run
  against a node.
- **An option's description is capped at 256 bytes.** It had no bound of any
  kind, and adding an ADDRESS option is permissionless: about one ERTH of fee
  plus a fifth of one in gas bought a megabyte of text that every node then
  decoded in every block, since the weight invariant walks every option. The
  same bound applies to genesis import, so an exported file cannot carry what a
  message could not have created.
- **`tx allocation` names the streams correctly.** Its help said the stream
  argument was `human` or `capital`; both were renamed and neither is accepted,
  so anyone following it got a flag error. It is `caretaker` or `groundworks`.
- **Genesis funds no devnet account.** The ads-for-gas hot wallet is out; its key
  had been on a laptop. **It therefore has no funds at height 1** and must be
  funded after launch from the validator. Only the genesis validator and the dex
  module hold anything.

### Added

- **An emergency fund in the Groundworks stream.** A second genesis option
  (`#2`, `community_pool`) that credits its accrued ERTH to the SDK community
  pool every block, so stake can build a governance-spendable reserve. It has to
  be an INTEGRATED option: the community pool is x/distribution's `FeePool`, not
  a wallet, so an ADDRESS option paying the distribution account would raise a
  balance nobody can spend — and that account is blocked to payouts besides.
  Seeded at genesis rather than left to a proposal, so it is votable from height
  1. Chains importing an existing genesis do not get it and need a
  `MsgAddIntegratedOption`.
- `docs/JOIN.md` — running a node.
- `docs/UPGRADES.md` — coordinated upgrades, written from a rehearsal.
- `docs/TRUST_STORE_RUNBOOK.md` — revoking a compromised passport certificate.
  Now also covers revoking a CSCA. Three things in it were wrong and are fixed:
  the revocation proposal named a `pubkey` field `MsgRevokeDsc` does not have,
  it pointed at an `earthd query pki dsc` command that does not exist, and it
  said revocation was not retroactive when it queues a purge that retires the
  signer's registrations.
- `scripts/build-genesis.sh` — the genesis is a build artifact now, not a file
  anyone edits.
- `scripts/rehearse-upgrade.sh` — runs a governance upgrade end to end locally.
- `LICENSE` — Apache 2.0. There was none, which legally meant nobody could use
  this.
- A documentation site at **[docs.erth.network](https://docs.erth.network)**,
  built with Docusaurus from `docs-site/`. Every page has an edit link, and the
  build fails on a broken link.

---

## [v0.1.6] and earlier

Released before this file existed. See the
[releases page](https://github.com/zenopie/earth-network-chain/releases) and the
commit history.

[Unreleased]: https://github.com/zenopie/earth-network-chain/compare/v0.1.6...HEAD
[v0.1.6]: https://github.com/zenopie/earth-network-chain/releases/tag/v0.1.6
