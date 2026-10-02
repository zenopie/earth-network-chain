# Orchard Phase 2 progress (chain privacy/orchard)

Checklist (from the Phase 2 brief): personhood, assembly, shieldedstaking,
dex on bundles; cleanup of legacy transfer code; gas-check; plan file.

## Done (committed)
- b49dd0b personhood + assembly on fee bundles; SignalOf = Sighash;
  x/shielded/testutil/bundle.go (Plan/ProveMsg/FeePlan, ForDir prover);
  TestPrivatePersonhood + bypass test pass on real proofs
  (x/personhood/testdata/app/actions = action proofs by public inputs;
  membership proofs re-recorded by name).

## In progress
- shieldedstaking (8 msgs; stake vote = two bundles)

## Next
- dex (+ USER DECISION: private LP shares, see below), cleanup, gas-check,
  plan file

## User decision (dex, add to Phase 2, commit separately)
LP shares are private: dexlp/<pool> registered as shielded assets;
MsgAddLiquidityShielded mints shares as a note to a pc (+ v2 ciphertext),
no transparent provider; new MsgRemoveLiquidityShielded: bundle releases
dexlp/<pool>; LpUnbonding without an account (payout pcs + ciphertexts for
both legs), both legs minted as notes at maturity; 7-day unbonding and
sweep halt-safety kept. Tests: add -> note shares; remove -> unbonding ->
both legs as notes; no account in events/state; wrong-pool shares refused;
genesis round trip.

## Decisions
- Fee-only msgs (personhood, assembly) carry `Bundle fee = 1`; their fee is
  the bundle's uerth balance, which must be its only balance (nothing else is
  released). No separate fee field: the digest already binds the balance.
