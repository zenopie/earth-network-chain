# Orchard Phase 2 progress (chain privacy/orchard)

Checklist (from the Phase 2 brief): personhood, assembly, shieldedstaking,
dex on bundles; cleanup of legacy transfer code; gas-check; plan file.

## Done (committed)
- b49dd0b personhood + assembly on fee bundles; SignalOf = Sighash;
  x/shielded/testutil/bundle.go (Plan/ProveMsg/FeePlan, ForDir prover);
  TestPrivatePersonhood + bypass test pass on real proofs
  (x/personhood/testdata/app/actions = action proofs by public inputs;
  membership proofs re-recorded by name).

- d1eb129 + bb6b0ab shieldedstaking on bundles: every msg `Bundle bundle`
  + explicit `fee` (bound by the sighash); moved value = release-map
  remainder of the msg's denom; MsgStakeVote = vote bundle (all anchors ==
  snapshot root, only derth/<valoper>) + fee_bundle (only uerth == fee);
  claim keeps fee_from_output. App tests pass on 82 re-recorded proofs.
  NOTE: app/dex_notes_test.go parked behind `//go:build dexpending` until
  the dex step removes it.

- 65fa337 dex on bundles: MsgNoteSwap{bundle, fee} (asset in = the one
  remainder; fee_from_output kept), MsgAddLiquidityShielded{bundle, fee}
  (one bundle, token + uerth legs). dex_notes_test unparked; 12 proofs.

- b6f0155 dex private LP shares: MsgAddLiquidityShielded -> share note
  (no provider); new MsgRemoveLiquidityShielded (LpUnbonding w/o address,
  withdrawal_id = 0x00||nf, both legs as notes); dexlp/ pool-locked (no
  unshield); 14 dex proofs.

## In progress
- cleanup (legacy_phase2.go, Transfer proto, zk/privacy transfer signals,
  fixture scripts), then gas-check, plan file

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
