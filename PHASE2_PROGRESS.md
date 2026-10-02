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

- Private-staking redesign (owner-locked stake note tree): mobile 2253c48
  circuits/stake; chain 441992e (VK + hashes), a26a006 (x/shielded:
  ExcludeAssetPrefix, bundle-less fee-from-output msgs, prover per
  circuit), 8126b2c (chain side), 81a5dc8 + TestStakeNotesOwnerLocked
  (app tests, 111 proofs). ORCHARD_DESIGN.md section 13.
- Verified at the last commit: go build/vet, go test ./... (all ok, zero
  orchard-phase2), make genesis-check, make privacy-vks-check, nargo test
  (stake 19, action 38, membership 11, privacy_core 9).

- Operator withdraw address (post-Phase 2 fix): 21c8f0d + tests
  (TestOperatorWithdrawAddrRefused, TestGenesisOperatorWithdrawAddr).
  Verified: go build/vet, go test ./..., make genesis-check,
  make privacy-vks-check CIRCUITS=../mobile-orch/circuits.

## Status
Phase 2 complete (incl. the user's three additions). Plan file updated.

## Decisions
- Fee-only msgs (personhood, assembly) carry `Bundle fee = 1`; their fee is
  the bundle's uerth balance, which must be its only balance.
- Staking/dex msgs carry an explicit `fee` (sighash-bound); the moved value
  is the release-map remainder of the msg's denom.
- Private LP withdrawals are keyed by withdrawal_id = 0x00 || first
  nullifier; payout mints both legs as notes; dexlp/* notes cannot be
  unshielded (x/shielded RegisterPoolLockedPrefix) but may be shielded in.
- Self-bond always compounds (SUPERSEDES "skip operators with a withdraw
  address elsewhere"): an operator's rewards always land in the operator
  account. Genesis sets distribution withdraw_addr_enabled=false (refuses
  every route: tx, authz, group, gov, ICA, wasm; only operators and the
  module hold delegations). Operator-scoped layers that survive a gov flip:
  ante WithdrawAddrFilterDecorator (top level + authz MsgExec);
  AfterValidatorCreated refuses an account with a foreign withdraw address;
  InitGenesis + `genesis validate` (app.ValidateOperatorWithdrawAddrs:
  staking validators + gentxs vs delegator_withdraw_infos) refuse one in
  genesis; compounding resets a foreign address
  (shieldedstaking_withdraw_addr_reset) instead of skipping. Only jailed
  validators skip. Open: an operator can still MsgWithdrawDelegatorReward
  its self-bond rewards mid-epoch (not refused; ask).
