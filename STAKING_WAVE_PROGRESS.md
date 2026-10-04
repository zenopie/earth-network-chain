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

## Wallet and backend follow-ups (not in this repo)
- Undelegate: send pc + ciphertext; drop the claim flow and claim-note
  scanning; watch shieldedstaking_unbond_payout / shielded_mint.
- Vote: 4-slot witness arrays, 10 public inputs (Android PrivacyProver VOTE
  7 -> 10; iOS likewise), weight = RoundVoteWeight(sum), one msg per
  validator; drop the spaced background vote run.
- Indexer: stake_vote event's vote_nullifiers; unbond payout events.
