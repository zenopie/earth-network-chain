# Staking wave: no background transactions

Branch privacy/orchard from f4a217c (chain) and c0d6a9b (mobile circuits).
User decision during the freeze: no background transactions and no
automatic fee spending. Fresh-genesis relaunch: no migrations. Principle:
people are private; power and public money are public. Wallet-facing rules:
ORCHARD_DESIGN.md section 18.

## Change 1: undelegations pay out by themselves
- [x] MsgUndelegate carries the payout pc (6) and ciphertext (7), bound by
  the sighash; queues an UnbondPayout against its epoch's record.
- [x] Payout at maturity by the EndBlocker (payouts.go): value x payout /
  requested, MintNoteSplit (2^63-1 a note), 50 payouts / 256 notes a block,
  retry with backoff (never dropped), due retries first. Start refusal: an
  undelegation worth more than 2^63-1.
- [x] Slashing economics preserved (pending target haircut, SDK entry
  slash, pro rata; dust to the community pool on the last payout).
- [x] MsgClaimUnbonding, claim notes, the unbond/ denom removed;
  MsgUndelegateResponse fields 1, 3 reserved.
- [x] Stake circuit unchanged (spc_mint is proven but unused for an
  undelegation): no stake VK change.
- [x] Genesis (unbond_payouts, next_unbond_payout_id), invariant 8,
  Query/UnbondPayout.
- [x] Tests: TestUnbondPayoutsSweepRetryNeverDrop (120 payouts over blocks,
  failures kept and retried, amounts, dust, one mint each),
  TestUnbondPayoutGenesisRoundTrip, lifecycle and slash pass-through via
  payouts (real proofs).

## Change 2: one stake vote per person
- [x] circuits/vote: MAX_NOTES = 4 under one nk, one weight <= sum, vnf 0
  for unused slots. 27,543 gates (2^15, bundled SRS 2^15 + 1). 37 nargo
  tests. Bundled into mobile android/app/src/main/assets/circuits (iOS
  references the same folder).
- [x] MsgStakeVote.vote_nullifiers (10, exactly 4), field 9 reserved;
  UsedVoteNullifiers; StakeVote.vote_nullifiers (7); event
  vote_nullifiers. Weight rounding check kept in ValidateBasic.
- [x] Positions not unified (public object; would tie hidden notes to it).
- [x] More than four notes: merge (MsgRestake) or a second vote for the
  rest (trade-off in section 18.2).
- [x] Vote VK regenerated (scripts/privacy-vks.sh), genesis.json rebuilt
  (sha256 1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8),
  staking proof fixtures regenerated.
- [x] Tests: TestStakeVoteManyNotesOneWeight (four notes one weight, a used
  note refused beside a fresh one, the fifth in a second vote, tally, shape
  rules, genesis), existing vote tests on the 4-slot witness.

## Change 3: private redelegation (freeze exception, user decision)
- [x] MsgRedelegate (src_validator 2, dst_validator 3, amount 4, stake 5;
  sighash StakeFields, Bytes(src), Bytes(dst), amount); response value,
  derth, position, completion_time. Stake circuit unchanged (spend
  derth/src with v_out = amount and change, mint derth/dst to spc_mint):
  no VK, fixture-format or genesis change (sha256 1824225d...7ee8).
- [x] Value out of src's queue first (book entry), the rest by x/staking's
  BeginRedelegate (module -> module) in the same block; derth/dst at dst's
  live rate for what arrived; rewards at both withdrawn into the queues
  first; dust (<= 0.001 ERTH) beyond the queue left to src's book.
- [x] Atomic in the private ante (ExecutesInAnte): x/staking's refusals
  (transitive, max_entries per pair) spend and pay nothing; checked again
  before any proof. Error 1120 ErrRedelegation. Query/Redelegation.
- [x] Slash of src during maturity: dst's book absorbs it pro rata; dst's
  undelegations in flight are set aside for the slash (ShelteredUnbondings,
  restored by the new BeginBlocker after x/slashing and x/evidence) so
  x/staking's unbonding-first order cannot hit them; rewards booked first;
  dst re-weighed at the block's end.
- [x] Votes: a derth/src note votes as src once (before or after the move);
  its derth/dst note cannot vote on proposals snapshotted before the move.
  Groundworks: positions untouched; a position moves by unlock,
  redelegate, lock. Self-bond redelegation still refused.
- [x] Genesis accepts the module's redelegations in flight, refuses any
  other; invariant 9.
- [x] Tests (app/redelegate_test.go): happy path with real proofs (bonded
  and queued, earning at dst at once, transitive refusal in CheckTx);
  max_entries; transitive refusal and expiry; slash during maturity
  (mutation-checked: without the set-aside or the reward booking it
  fails); votes before and after the move; Groundworks weight moving A ->
  B; genesis round trip; invariant 9. 53 new proofs;
  scripts/staking-fixtures.sh runs TestRedelegate.
- [ ] Known limit, documented (section 19.3): the module is one delegator,
  so x/staking's transitive rule and max_entries are shared; dust
  redelegations can grief a validator's private stakers out of
  redelegation (not undelegation) for 21 days. Fix ("lanes": several
  delegator accounts) deferred.

## Change 4: one stake note per validator; note-enforced slash debt (user decisions)
- [x] circuits/stake v2 (mobile 0ec5e4c, f02ec61): two lanes (lane A 2 in /
  1 out with v_in and v_out; credit lane 1 in / 1 out), padding inputs and
  outputs, slash labels (keep or clear at the debt tree), 16 public inputs,
  16,242 gates (2^14), 52 nargo tests. spc_mint gone.
- [x] circuits/vote v2: 2 slots, labelled notes at their debt-adjusted
  value, debt_root public; 21,716 gates (2^15); 45 nargo tests.
  privacy_core: stake_cm with label, stake_label, debt_leaf,
  debt_retained; Go parity (zk/debt TestNoirParity). Bundled stake.json,
  vote.json rebuilt.
- [x] The chain mints no stake note: MsgDelegate.derth / MsgRedelegate.
  dst_derth named by the wallet, checked against the live rate
  (checkCredit); unlock merges the position's derth. Restake merges only.
- [x] Redelegation without BeginRedelegate (moveBonded, recordEntry): no
  transitive lock, no max_entries; one entry per block per pair, 4,096 cap
  (joining the latest entry, which keeps its height and completion).
- [x] Slash debt: labels (move key = credit nullifier, move_time, exposed),
  window = longest unbonding seen + 600 s, zk/debt indexed tree (rows for
  slashed moves only), slash watch (BeforeValidatorModified + Unbond count,
  settled in the BeginBlocker), dst supply cut so the rate holds,
  per-move retained, invariant 10, genesis moves/debt_rows/max_unbonding.
- [x] Votes: snapshot rule unchanged (the pre-merge note votes the
  pre-existing value; the merged note cannot; no double vote).
- [x] Tests: TestRedelegateSlashDebt (slash before the move, evidence
  after; dst rate unchanged; the exposed note merged with a top-up keeps
  its label, cannot leave, votes its haircut value, clears at the window's
  end and pays exactly its value), TestRedelegateSlashDuringMaturity,
  TestRedelegateMergeRules, TestRedelegateNoLockout (griefer inbound, 41
  entries), TestRedelegateEntryCap, TestRedelegateVoteSnapshot (top-up
  after the snapshot), TestRedelegateMovesStakeWithoutGap,
  TestRedelegateGroundworksWeight, TestRedelegateGenesisRoundTrip,
  TestRedelegateInvariant; the whole staking suite on the new harness;
  zk/debt and types unit tests. VKs, staking (258) and dex proof fixtures,
  genesis sha256 ffb269c5047e823b3f3aa27034767ff894c9fe76626703ccf42d0b1b321b6b59.
- [x] Audit 7 (module D) items: both books re-weighed after a
  redelegation, the source too after a slashed redelegation; pruned options
  dropped from position splits (export) and re-filed voters; D7-L2
  bonded-only Groundworks weight; D7-L1 gov blocked.
- [ ] Known limits: past 4,096 maturing entries for one pair, an infraction
  between the latest entry's height and a joining move's falls on the
  source's stake; exposed derth waits up to the unbonding time before it can
  be undelegated, locked or redelegated on (x/staking's own per-delegator
  transitive rule, now per note).

## Wallet and backend follow-ups (not in this repo)
- Stake v2 (ORCHARD_DESIGN 20.8): one note per validator (merge on every
  delegate, unlock, redelegate credit); padding nullifier and zero-note
  outputs; quote derth with a margin; StakeProof fields (commitment,
  ciphertext, credit_*, clear_before, debt_root); 201-byte stake
  ciphertexts with the label; track labels (move_key = the credit
  nullifier) and clear them after the window (Query/DebtTree, zk/debt
  witness); Android PrivacyProver STAKE 11 -> 16 public inputs, VOTE 10 -> 9
  (2 slots + debt_root); iOS likewise.
- Indexer: `shieldedstaking_debt_row` events (the debt tree's stream),
  `slash_debt`, `move_slashed`; Query/Redelegation is gone.
- Undelegate: send pc + ciphertext; drop the claim flow and claim-note
  scanning; watch shieldedstaking_unbond_payout / shielded_mint.
- Vote: 4-slot witness arrays, 10 public inputs (Android PrivacyProver VOTE
  7 -> 10; iOS likewise), weight = RoundVoteWeight(sum), one msg per
  validator; drop the spaced background vote run.
- Indexer: stake_vote event's vote_nullifiers; unbond payout events.
- Redelegate: build MsgRedelegate (ORCHARD_DESIGN 19.7), check
  Query/Redelegation first, find the derth/dst note by its stake note event
  (spc) or trial decryption. Indexer: shieldedstaking_redelegate events;
  failed redelegations leave no trace (refused in the ante, nothing spent).
