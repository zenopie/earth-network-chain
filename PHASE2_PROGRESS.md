# Orchard Phase 2 progress (chain privacy/orchard)

Checklist (from the Phase 2 brief): personhood, assembly, shieldedstaking,
dex on bundles; cleanup of legacy transfer code; gas-check; plan file.
Added by the user: private LP shares (dex), self-bond auto-compound
(staking), then the private-staking redesign (stake note tree, below).

## Done (committed)
- b49dd0b personhood + assembly on fee bundles; SignalOf = Sighash;
  x/shielded/testutil/bundle.go (Plan/ProveMsg/FeePlan, ForDir prover);
  TestPrivatePersonhood + bypass test pass on real proofs
  (x/personhood/testdata/app/actions = action proofs by public inputs;
  membership proofs re-recorded by name).
- d1eb129 + bb6b0ab shieldedstaking on bundles (main-pool derth notes):
  every msg `Bundle bundle` + explicit `fee`; MsgStakeVote = vote bundle
  (all anchors == snapshot root, only derth/<valoper>) + fee_bundle. 82
  proofs. (Done before the "don't port staking yet" ordering note arrived;
  it is SUPERSEDED by the redesign below, which replaces these msgs.)
- 65fa337 dex on bundles: MsgNoteSwap{bundle, fee}, MsgAddLiquidityShielded
  {bundle, fee} (one bundle, token + uerth legs).
- b6f0155 dex private LP shares: MsgAddLiquidityShielded -> share note
  (no provider); new MsgRemoveLiquidityShielded (LpUnbonding w/o address,
  withdrawal_id = 0x00||nf, both legs as notes); dexlp/ pool-locked (no
  unshield); 14 dex proofs.
- e4f9d10 gas-check: MsgRegister with fee bundle decodes; CheckRegistration
  unchanged; tests.
- 56864c8 cleanup: legacy_phase2.go, private_phase2.go stubs, Transfer
  proto, ErrInvalidTransfer, zk/privacy transfer signals gone; zero
  TODO(orchard-phase2).
- 4ca59eb self-bond auto-compound at epoch end (uerth rewards, not
  commission; per-validator guarded; jailed skipped); TestSelfBondCompounds.
- Verified at 4ca59eb: go build, go test ./... (all ok, no orchard skips),
  make genesis-check, make privacy-vks-check.

## In progress
- plan file update (step 7), then the private-staking redesign.

## Next: private-staking redesign (user-approved spec, own commits)
derth NON-TRANSFERABLE. Separate append-only stake note tree in
x/shieldedstaking (own root window, snapshot roots for votes, own
nullifier set). Stake note = H(TAG_STAKE, validator_id, amount, owner_pk,
rho, rcm). derth/unbond leave the main pool's asset registry/turnstile; dex
refuses them. New Noir circuit `stake` (mobile-orch circuits/, nargo tests
incl. negative): input stake note (or none), outputs owner-locked to the
input's owner_pk, public validator/amounts, binds the sighash. Ops:
delegate (ERTH bundle -> stake note for owner proven by nk), merge/split
(same owner), undelegate (-> owner-locked unbond claim until maturity, then
an ERTH main-pool note), stake vote (snapshot stake root, re-create same
note), positions (owner proof replaces the one-time key). Fees: ordinary
ERTH fee bundle. Rates/epochs/compounding/slashing/tally/Groundworks/self-
bond compounding unchanged. Tests with real proofs (see brief). Update
ORCHARD_DESIGN.md + plan, incl. the future option to bind stake notes to a
personhood identity if account selling appears.

## Decisions
- Fee-only msgs (personhood, assembly) carry `Bundle fee = 1`; their fee is
  the bundle's uerth balance, which must be its only balance.
- Staking/dex msgs carry an explicit `fee` (sighash-bound); the moved value
  is the release-map remainder of the msg's denom.
- Private LP withdrawals are keyed by withdrawal_id = 0x00 || first
  nullifier; payout mints both legs as notes; dexlp/* notes cannot be
  unshielded (x/shielded RegisterPoolLockedPrefix) but may be shielded in.
- Self-bond compounding skips operators whose withdraw address is another
  account (rewards go where they asked).
