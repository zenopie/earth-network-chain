# Audit history (privacy/orchard)

Every audit round and implementation wave on the privacy/orchard branch, in
order: what was found, how it was fixed, the commits, and what was accepted
instead of fixed. The current design is [ORCHARD_DESIGN.md](ORCHARD_DESIGN.md);
operator-facing changes are in [CHANGELOG.md](CHANGELOG.md). Audits before the
privacy relaunch (v0.9.x and earlier) are recorded in CHANGELOG.md only.

Every round below shipped as a fresh genesis with no state migration. Auditor
PoCs were ported as regression tests that assert the safe outcome.

Severity column: as the auditor graded it; `-` where the report gave none.
A finding marked *superseded* was fixed and the fix later replaced by a
redesign; the replacement is named.

## Genesis and verifying-key checkpoints

| After | genesis.json sha256 | Circuits changed |
| --- | --- | --- |
| Fix round 2 | `c2734154…f5c1` | none |
| Fix round 3 | `d5385d77…54db` | none |
| Fix round 4 | `b201c96b…65cd0` | membership (predecessor_at) |
| Fix round 5 | `67a6ed4a…3212`, then `77af7586…652d` | action, stake, vote (2^63-1 bound) |
| Fix round 6 | `77af7586…652d` (unchanged) | none |
| Staking wave, change 2 | `1824225d…7ee8` | vote (4 slots) |
| Staking wave, change 4 / audit 7 | `ffb269c5…6b59` | stake v2, vote v2 |
| Pre-audit: vote padding | `84921c0b…7acc` | vote (padding nullifier) |
| Passport coverage | `acb96128…be1c` | 33 register circuits replace the seven |
| Final-audit fixes | `a381e2c9…4731` | none (keys unchanged; personhood genesis drops the moved-out sets) |

## Wave: Orchard phase 1 (2026-10-02)

Orchard-style bundles replace the 3-in/3-out transfer circuit in x/shielded.

| Change | Commits |
| --- | --- |
| Bundle spike (design, Grumpkin, binding signature) | 466d952 |
| zk/orchard production package (per-action anchors, sighash with bundle count, deterministic parallel VerifyProofs, value-base cache); bundle fixtures 1/2/3/10 | 98b5e03 |
| x/shielded on bundles (proto, keeper, ante, testutil scenario, 21 proofs); phase-2 modules stubbed | 1c4802a |
| Action VK in genesis, config.yml and testdata; app tests on bundles | d015f17 |
| Transfer fixtures dropped | 8f31e06 |
| Design doc: production deltas | 9cb7422 |

## Wave: Orchard phase 2 (2026-10-02)

| Change | Commits |
| --- | --- |
| Personhood and assembly on fee bundles; membership proofs bind the sighash | b49dd0b |
| Shieldedstaking on bundles (replaced by the stake-note redesign below) | d1eb129, bb6b0ab |
| Dex on bundles; private LP shares (MsgRemoveLiquidityShielded, `dexlp/` pool-locked) | 65fa337, b6f0155 |
| gas-check decodes MsgRegister with its fee bundle | e4f9d10 |
| Legacy transfer code removed (zero `TODO(orchard-phase2)`) | 56864c8 |
| Owner-locked stake note tree (circuits/stake, own tree and nullifiers) | 441992e, a26a006, 8126b2c, 88b1763, 81a5dc8, 34b5415 |
| Self-bond and commission compound; no claim on any route | 4ca59eb, 21c8f0d, aa78c11, 98e164d |
| Per-validator reward escrow (operator income never liquid) | 291a5cd, f8536b4 |
| Readable errors for blocked SDK msgs (1110, 1116, 1117); unused registered errors removed | 6c0f94b |

Left: dropping the proto `reserved` statements of retired fields (field
numbers stay reserved either way).

## Audit 1a: x/shieldedstaking (branch fix/staking, merged 9e86f75)

| ID | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| F0 | Critical | Non-canonical (case-aliased) valoper strings split one validator into several books | `CanonicalValoper` in every ValidateBasic, keeper and genesis; same sweep for MsgSend.receiver, affiliate and allocation/dex addresses | 994d307, 7e25240 |
| F1 | High | Epoch end starved by many books | Epoch sweep with a cursor (EpochValidatorLimit per block); `min_delegation` 1 ERTH | 36259ef |
| F2 | Med-High | Gov snapshot gas grows with books; AssertInvariants unbounded | O(1) snapshots (copy-on-write SupplyCheckpoints); invariants bounded by InvariantBookLimit, recovered | 36259ef |
| F3 | Medium | Zero-height export broke the books | BookRewardsForZeroHeight, ResetHeightsForZeroHeight | 11aadce |
| F5 | Low | Orphaned backing captured by a new delegator | Delegation to a settling book refused; orphans to the community pool | 11aadce |
| F7 | Low | Retired validator's escrow never released | Retirement queue releases after unbonding_time | 11aadce |
| F8 | Low | MaturityLimit / max_entries floor | MaturityLimit removed; floor checked at InitGenesis and UpdateParams; failed releases retried | 11aadce |
| F9 | Low | UpdatePosition with splits and zero weight | Refused | 11aadce |
| - | - | Undelegate against a settled current-epoch record | Refused (defensive) | ccdf9f3 |
| Pos | Low | Position count and cap | *Superseded* by per-validator Groundworks weighing (audit 1c, G) | 0fcd99e |

Accepted: F4 (donation inflation) harmless under the min-derth rule
(regression test 6b4103e); F6 (vote of undelegated stake keeps a diluted
voice) documented in votes.go. Not changed: app `genesis_withdraw.go` foreign
map and the export jail allow-list (operator tooling, validation only).

## Audit 1b: personhood and assembly (branch fix/person, merged a7f69ab)

| # | Finding | Fix | Commits |
| --- | --- | --- | --- |
| 1 | A revocation mixed with other msgs or nested in a wrapper got human votes | Classified as a refusal | 077a818 |
| 2 | Replayed MsgRegister zeroed the holder's leaf; relayer could swap ciphertexts | Switch to the live idc refused (1123); RegistrationBinding covers both note ciphertexts | ce8846e, f9c7d3f, 984b8ac |
| 3 | Transparent gas grant (gas-check membership) | Removed | 1ea5926 |
| 4 | Revoked-signer purge starved the other sweeps | Reserved budget/8 per sweep, two-round sharing | 19e29e0 |
| 5 | Removal ballots repeatable at will | 30-day cooldown per option (constant, in genesis) | 4184520 |
| 6 | Dead `PrivateAnchorAcceptor` hook | Removed | 07ec6d4 |

## Audit 1c: x/shielded (+ Groundworks lazy weights)

| ID | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| M1 | Medium | Relayer could rewrite memo, timeout_height, gas_limit; proofs accepted trailing bytes | Tx fields bound in every sighash; timeout_timestamp refused; proofs exactly 14,656 bytes; canonical bech32 | cb1aba3 |
| M2 | Medium | Junk tx cost a node every proof in CheckTx | Cheap checks first, sequential verification stopping at the first failure | cb1aba3 |
| L1 | Low | Block cap vs failed txs | Fixed private gas consumed before proofs; block gas bounds verification | 8151396 |
| L2 | Low | Pool could mint/release/pay fees to itself | Refused | 8151396 |
| L3 | Low | Pool account had Minter/Burner | Removed | 8151396 |
| L4 | Low | Ante events persist on failed txs | Documented for indexers | 8151396 |
| L5 | Low | Release map not checked against the handler | Handlers declare `ReleasedDenoms`; exact set required | 8151396 |
| Info | - | Priority overflow; bb stderr; uppercase receiver | Priority capped; bb log level 3; canonical receiver | cb1aba3 |
| G | (user) | Groundworks position weights | One weighted voter per validator, no position cap, min_position 1 ERTH | 0fcd99e |
| A, B | (user) | Note discovery; fee rules | Blind ciphertext on every minted note; fee = uerth balance less what the msg moves | b330b33, fb3443b, 203847a |

## Audit 2 (chain re-audit)

| ID | Finding | Fix | Commits |
| --- | --- | --- | --- |
| R1 | Registration A->B->A replay | UsedBindings, swept, in genesis; registration_sweep_limit validated | 3f93622 |
| R2 | CheckTx bypass: valid bundle, junk action proof | Msg's own proofs first; CheckTx-only verified-proof cache | f54124f |
| R3 | Vesting validator operators | Refused; compounding never moves spendable balance | 8b23fc2 |
| R4 | Tally attribution after post-snapshot stake | Deduction capped at voted derth x current rate | 89c5742 |
| R5 | GwEpoch index leak; epoch-end reweigh unbounded | Entry dropped with the last position; reweigh in the bounded sweep | 86625d4 |
| R6 | Non-canonical proof elements (x and x+r) | Refused | 4da26f6 |
| R7 | Private tx malleability (tip, re-encodings) | Tip refused; canonical byte encoding required | 2db94fb |
| R8 | max_entries / unbonding floor at epoch end | Floor re-checked; undelegation deferred past max_entries | acc35aa |
| - | Removal cooldown on carried ballots; invariant 5 walk; escrow export | Only declined ballots grant it; walk bounded; pending releases and retiring escrows exported | ea6fbc7 |
| - | Stake proof ciphertext slots; slash raising the epoch rate | Exactly 2 slots; post-slash rate = min(live, epoch) | 2e9dfe5 |
| - | Ciphertext lengths | Exact: action 217 bytes, stake 153 (now 201, staking wave) | d593bae, 871b284, ffa7821, 4a6066b |

## Audit 3

| # | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| 1 | High | Escrow poisoned by a locked account at its address | Move only spendable coins; failed releases rotate (cursor, 24h retry) | 987c688 |
| 2 | - | Unshield into module accounts | Refused; staking takes pool coins only via ReleaseToModule | 7a259c5 |
| 4 | Medium | gas-check blind to revocations (empty KeySet values) | abci_query with proof, ics23 exist bit | 0b99fc6 |
| 5 | Medium | Demoted expedited proposal lost its subjects | Kept until x/gov ends voting | e1915cd |
| 6 | - | Configurable app mempool could fork | No-op mempool forced | 5264711 |
| 7 | - | Unbond record growth walked at epoch end | Orphan records indexed | 22b010a |
| 8 | - | Non-canonical vote weight strings | Canonical LegacyDec required | 2aa9449 |
| 9 | - | Genesis root records unchecked | Checked against the rebuilt tree | 3e4a0aa |
| 10 | Low | Personhood: binding hold, YYMMDD, genesis completeness, activation margins | Held to max skew; round-trip; exported; constant 1-day margin and LeaseHold | 3858639, a9b5525 |
| 10 L6 | Low | Referrer bound without consent | Consent signature; *superseded* by handles (audit 4, item 7) | 6bea749 |
| 11 | Low | Dex zero-leg deposit; buyback trade size | Refused; per-trade cap `buyback_max_trade_seconds` | 01e9e7d, a533df0 |
| 12 | Info | Staking module perms; genesis delegations; private cap in proposals | No Minter/Burner; module/self-bond only; PrepareProposal respects the cap | 03012a3 |

Dropped: item 3 (stake votes on concurrent proposals). Revealing the spend
nullifier at each vote links votes; solved properly by the vote redesign
below.

## Wave: stake votes without spending (2026-10-03)

Indexed stake nullifier tree, circuits/vote (per-proposal vote nullifier,
non-membership at the snapshot), MsgStakeVote that spends nothing.
Commits 31b95e1, f2b38b9, 96c06a0, 816e9d6, 5b8dd6e (invariant 7), 9b29f5d.

## Audit 4

| # | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| 1 | High | Dex overflow halts the chain | Pool cap 2^120; big.Int arithmetic; per-entry hook work on recovering cache branches (internal/safeexec) across modules | dc22d1b, 2628d59 |
| C2/C4 | - | Deposit legs rounding; drained pools fail validation | Rounded up; 0/0 pools validate | dc22d1b |
| C3 | - | Genesis identity roots unchecked | Must equal the rebuilt tree's root; not after genesis | 8a5b3da |
| C5 | - | Buyback params | window <= max trade <= accrual on effective values | 167b586, e781ea0 |
| C6 | - | Proposal subjects lost on export | Exported and validated | 4928d3f, 4ff3997 |
| C7/C8 | - | Activation bound inclusive; lapsed caretaker leases over-credited | Exclusive bound; stream settled to the lease's expiry on its own budget | de4638e |
| C10 | - | Referrer consent replay | Consent v2; *superseded* (consent removed with public referrers, item 7) | 167b586 |
| L1, I1, I3 | - | Expired timeout_height; cap doc; verifier panics | Refused in CheckTx, dropped in PrepareProposal; panics are action errors, ante panics keep gas | 11d0445 |
| G1, G2, L-A, I1, I2 | - | Staking genesis roots, module withdraw address, root recording, snapshot supply | Checked/refused; one guarded call; supply at block start | d17052e |
| 7 | (user) | Referral codes and public referrer addresses | Replaced by handles (MsgBindHandle; MsgBindReferrer, consent, codes removed) | 4b94dcd (codes, then removed), 9d016f0, 6e5924f |
| 7b | (user) | Predecessor-aware activation; handle and caretaker moves | Leaf predecessor_at, max_predecessor input (membership VK), MsgMoveHandle, MsgMoveCaretaker | af30f73, 4a663d5 |

## Audit 5

| ID | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| A1 | High | Struck INTEGRATED option revived across export/import | Kept out of the handler set; prune clears; dangling ids dropped | 08afd1e |
| P1 | High | Registrant could direct the referral half to itself | Chain mints the referral note to the handle's owner_pk with a derived, published opening | 87f3480, c5b11f6 |
| D1 | High | LP payout above u64 halted / was dropped | MintNoteSplit (<= 128 notes of 2^63-1); retries with backoff, never dropped; start cap | 49b8552 |
| P2 | Medium | Two live handles per passport | Claim bound unless holding a live handle; moves need a live handle | 4551a8c |
| P3 | Medium | Wallets computed bounds from mutable params | Query/LeaseBounds | 456ae91 |
| L-P5, L-SH2 | Low | Bind gas; shield of send-disabled denom | Nine note writes; refused | d2a5878 |
| L-SH1 | Low | Anchors about to lapse | 120 s margin in CheckTx; PrepareProposal drops lapsed | 4f8cf3c |
| L-AS2, L-SH3 | Low | Chamber votes vs circuit breaker; gas params uncapped | Pass the breaker; caps (proof 10M, note 1M, bundle 1M) | a080ece |
| L-DX3, L-DX4 | Low | TWAP not exported; swap fee rounding | Exported; rounds up | 694d81c |
| L-ST1, L-ST2 | Low | Checkpoint walk; genesis position derth | Stops early; validated, fits a note | 425f517 |
| L-AS1, L-AS3, L-AS4 | Low | Demoted expedited round scope; tally bar; stale ballots | New nullifier scope; expedited bar; cursor sweep | 94f09dc |
| (D1 follow-up) | - | Wallets hold notes only up to 2^63-1 | Every chain mint capped at 2^63-1 | 8ed1278 |
| (D1 follow-up) | - | Bundle outputs could reach 2^64-1 | 63-bit note bound in action, stake and vote circuits; new VKs | 2202ba3, d47e5cb, e0f955f, d9d2366, c0ad1dd |

Skipped: L-P4 (inherent: a handle must resolve, referral recipient and amount
are public by design); L-P6 (commit-reveal only if names gain value); L-DX2
(needs a batched cursor or pool-creation fee, a design choice); the
"max-shape gas <= max_gas" part of L-SH3 (max_gas is a consensus param
Params.Validate cannot see); info: AuthorizedNullifiers kept (tests use it).

## Audit 6

| ID | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| D6-1 | High | Groundworks weight stayed after a self-bond left a non-bonded validator | Weight = bonded sum with the removed delegation left out | d80708d |
| B6-1 | Medium | Identity switch under a different DSC escaped rate caps and purges | Refused (1127); switches count against the signer's daily cap | db5146c |
| B6-4 | Low | Registration replayable across networks | Binding includes Bytes(chain_id) | db5146c |
| B6-2, B6-3, B6-5, B6-7 | Low | Stale root by-time entry; genesis dates and leases; unretirable registration blocks the sweep; nullifier length | Fixed; refused; passed over for a day; 1..32 bytes | 13232b1, 3fe5c61, 5a57a2d |
| A-L1 | Low | Inflated private gas_limit | At most 5x the gas used | 423bc6d |
| A-L2 | Low | SendEnabled at pool edges | Checked on releases and mints | 6cef274 |
| A-L3, A-I5, A-I1 | Low | Mixed anchors per bundle; cap; soundness comment | One anchor per bundle; cap <= 256; comment | 093a6fc |
| A-I2 + wallet | Info | Handle owner not reported | HandleEntry.owner, event owner fields | 2fab209 |
| C-L1..C-L4 | Low | Slashed validator without a book; lock above a note; exact vote weight; snapshot after failed root recording | Skipped; fitsNote; 3 significant digits; no roots taken | 3d9200b |
| D-L-A1 | Low | Pruned options in voter splits | Dropped at resync and export | 84662c6 |
| D-L-D1, D-L-D2, D-L-AS1 | Low | LP sweep note budget; volume depth cap; subject sweep | Payout waits; <= 1,000; cursor | 7afdc27, b501250, a1eba52 |
| C-I2, D-I, A-I3 | Info | Module delegation without a book; unused Staking perms; bb error read thread | Refused; removed; OS thread locked | 56a5f6d |

Accepted: X-1 (transparent tx replay across earth-1 relaunches; the user was
the only user of the earlier chains; account-number offset written and
reverted). Skipped as wallet behaviour: A-I6 (fee rounding), B6-6
(randomised delays). Inherent or by design: C-I1 (nk sellable off-chain),
C-I3 (contract, ICA, group operators), C-I5 (dust undelegations), I-AS2
(single YES carries an uncontested removal ballot). Documented only: A-I4
(bb assumptions), C-I4 (stake votes keep the expedited-round vote after
demotion), B6-5 lag (membership proves until the expiry sweep zeroes the
leaf). Not changed: I-D3 (TWAP LegacyDec precision; feature-sized), I-D4
(export gap credited at the pre-export price, arguably right).

## Wave: staking without background transactions (2026-10-03 .. 10-04)

| Change | Commits |
| --- | --- |
| 1. Undelegations pay out by themselves; MsgClaimUnbonding and claim notes retired | 4f05cef, 5042f13 |
| 2. One stake vote per person (4 slots, one weight) | 42a28e4, 5042f13 |
| 3. Private redelegation via BeginRedelegate (*superseded* by change 4) | 6918d7f, 8d2afcf, bd2a810, 47a02fa |
| 4. One stake note per validator; module-recorded redelegation entries; note-enforced slash debt; stake v2 and vote v2 circuits | 06fd915, ec536f3, 49ac5bb |
| Docs | 48b631c, 0c60692, dff3a9b |

Change 3's known limit (one pooled delegator: x/staking's transitive rule and
max_entries shared, griefable) is gone with change 4.

## Audit 7 (staking and Groundworks, 2026-10-04)

| ID | Sev | Finding | Fix | Commits |
| --- | --- | --- | --- | --- |
| A7-1 | Medium | Redelegation drew the unslashable queue first, escaping a coming slash | Value leaves the source pro rata (queue share u x P / (D - U + P)); queue first only for an Unbonded source or one with no bonded stake | be780c5 |
| (found with A7-1) | - | At the entry cap a move joined an old entry and escaped a later infraction | Two oldest entries merge; every move gets its own entry; cap 1,024 | 17224a6 |
| A7-2 | Medium | Zero-height export with open moves failed re-import | Moves dropped, debt rows kept; height-0 entries exempt from share checks | e52b170 |
| A7-L1 | Low | Redelegation gas and invariant cost with many entries | Gas per entry (+ merge surcharge); records decoded once per pass | 17224a6, e52b170 |
| A7-L2 | Low | Slashed entries guessed as "the last N" | Replay x/staking's SlashRedelegation; must match unbonds and burnt shares | 2af2008 |
| B L-1 / A7-L3 | Low | A clearing proof was distinguishable | Every stake proof names the current clear_before and debt_root | c563a2a |
| B L-2 | Low | Owner-tag salt reuse links txs | Documented wallet rule (fresh per non-position proof) | b46a4bb |
| D7-L1 | Low | Gov module account not blocked | Blocked | 7129b1d |
| D7-L2 | Low | Self-bond weight at non-bonded validators | Counted only at Bonded; resync on bond/unbond start | fd79d39 |
| D items | - | Books of a redelegation and slashed source not re-weighed; pruned options in position splits | Re-weighed in the block; dropped | c107ef9, 6f67a2d |
| Infos | Info | 201-byte comment; redelegation value bound; genesis move checks | Fixed | e52b170 |

## Pre-audit fixes (2026-10-04)

| Change | Fix | Commits |
| --- | --- | --- |
| Per-validator book queries tied an IP to an intent | Query/Validators: every validator's quote inputs, paged | b7e77f8 |
| Launch ceremony was manual and refused the launch key | scripts/ceremony.sh; TestLaunchCeremony with a pending path | 5343560 |
| Docs: LP share transfer, handle renewal, fee split | Answered in ORCHARD_DESIGN (5.3, 6.2, 10) | a838682 |
| A one-note stake vote showed a zero vnf | Unused slot carries a padding nullifier H(TAG_VPAD, nk, r, proposal_id); chain requires exactly 2 non-zero, distinct vnfs, all recorded | bbbcb92 |

## Known limits and open items

- Exposed (labelled) derth waits up to the unbonding time before it can be
  undelegated, locked or redelegated on (x/staking's per-delegator
  transitive rule, applied per note). By design.
- A double sign at the first block after a zero-height export slashes the
  height-0 entries, whose moves were dropped: that slash falls on the
  destination's book. Accepted (A7-2).
- Standing audit assumptions (ORCHARD_DESIGN, soundness): bb's MSM black box
  for witness points; gnark-crypto grumpkin is partially audited; the
  value-base sign check is the only defence against the −G inflation case.
- **Open, needs the user (audit 6):** the launch ceremony. `accounts.json`
  still holds the devnet faucet (`earth1s7rgs…`) and gas wallet
  (`earth1jtc2z…`) and the placeholder validator; `scripts/ceremony.sh
  --genesis-time <RFC3339> --pubkey <json>` removes them, swaps in
  `earth1n6amvk…`, signs the gentx and rebuilds genesis. `TestLaunchCeremony`
  reports PENDING CEREMONY until it has run.

## Wave: passport signature coverage (2026-10-04)

Registration covered seven SHA-256-only circuits (RSA PKCS#1 v1.5 with
e = 65537, five curves). Research (mobile `circuits/PASSPORT_COVERAGE.md`:
ICAO 9303-12, the ICAO PKD's unexpired DSCs via Self's map data, Self,
Rarimo and zkPassport production circuits) found PSS, e = 3 and random
exponents, P-224/P-521/brainpoolP224r1, SHA-1/224/384/512 and mixed hash
profiles in real, valid passports. Now 33 variants, about 99.8% of PKD DSCs.

| Change | Commits |
| --- | --- |
| x/pki: P-224 and brainpoolP224r1 (tags 8, 9); RSA commitment Poseidon2(10, e, n), tag 7 retired; explicit ECParameters matched on every parameter (was prime and order); RSA-PSS mask/trailer refused unless MGF1 over the message hash and 1; sha224WithRSA | 00db9b1 |
| privacy-vks.sh writes and checks the 33 passport keys; regen-poa-fixtures.sh proves each variant from the shared synthetic passport; poafixtures and personhood-fixtures on lean_poa_p256_sha256 | c41f125 |
| Genesis: 33 passport verifying keys | 9f09c6c |
| Tests: every variant's proof verifies and its dsc_key equals the chain's commitment over its DSC certificate; genesis seeds exactly the variant set | e2f3e18 |
| x/personhood passport and app proof fixtures on lean_poa_p256_sha256 | c75b20d |

Findings in the old circuits, fixed with the change (mobile repo):
- The 200-byte eContent buffer refused any SHA-256 SOD with more than four
  data groups (many EU passports).
- Android fetched a 2^18 SRS, so the P-384, BP384 and BP512 circuits
  (2^19) could not be proved there.
- The brainpoolP512r1 circuit hashed with SHA-256; the BP512 passports seen
  sign with SHA-384/512.
- noir-bignum predated its external audit's fixes (noir-bignum#270). The old
  RSA modulus binding was still sound (each limb range-checked by
  `to_be_bytes`).
- The SHA-1/384/512 libraries read a BoundedVec's storage past its length,
  and `from_parts` does not clear it: junk there changed the digest (a wrong
  answer, not a forgery). The wrappers now zero the tail.

Accepted: SHA-1 (issuer-formed inputs: a forgery needs a second preimage;
removable by governance once the last such passports expire, about
2027–2028). Excluded: RSA-1024 (ICAO specimen only), brainpoolP320r1 and
secp192r1 (no unexpired DSC; P192 too weak), twisted Brainpool curves, DSA,
PSS with SHA-1 or a mismatched MGF1, hash profiles not seen in real SODs,
SODs over the size maxima. The prover may name any variant: a hash or
padding the passport does not use needs a preimage or a forged signature,
and the key type is bound by the commitment.
