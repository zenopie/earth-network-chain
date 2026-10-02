# Orchard Phase 1 progress (chain privacy/orchard, mobile privacy/orchard)

## Done (committed)
- mobile `privacy/orchard` (wt/mobile-orch): 3334cc2 action circuit finalized (38 tests,
  headroom documented, 8,120 gates); bf69fe3 transfer circuit removed from the workspace.
- chain `privacy/orchard` (wt/chain-orch):
  - 98b5e03 zk/orchard production (per-action anchors, sighash with bundle count,
    parallel deterministic VerifyProofs, ValueBase cache, refusal tests); zk/ultrahonk
    orchard fixtures 1/2/3/10.
  - 1c4802a x/shielded bundles (proto, types, keeper, ante, testutil scenario + prover
    cache, 21 real proofs in x/shielded/testdata/proofs); Phase-2 modules stubbed
    (`private_phase2.go` per module, `x/shielded/types/legacy_phase2.go`).
  - d015f17 action VK in genesis/config.yml/testdata, networks/genesis.json rebuilt,
    app tests on bundles; Phase-2 app suites skip with TODO(orchard-phase2).
  - 8f31e06 transfer fixtures dropped from tools/privacyfixtures and zk/ultrahonk.

  - 9cb7422 ORCHARD_DESIGN.md section 12 (production deltas); zk/privacy legacy
    signals marked TODO(orchard-phase2).
- Verified: `go build ./...`, `go test ./...` (all ok), `make privacy-vks-check`,
  `make genesis-check`, `nargo test` (action 38, privacy_core 9, membership 11).

## Status
Phase 1 complete. Next is Phase 2 (see the report / ORCHARD_DESIGN.md section 12).

## Decisions
- Anchor per action (not per bundle); bundle 0's actions may use the
  PrivateAnchorAcceptor (stake votes), other bundles must be in the window.
- Sighash = Signal(type URL, chain id, K, digest_0..K-1, msg fields); digest binds
  every action's anchor/nf/cm/cv/ciphertext and the balances (asset ids).
- MsgSend{bundle, receiver, fee}: fee from uerth balance; every remainder goes to
  receiver, paid in the ante (atomic). fee_from_output for MsgSend is the uerth
  remainder itself; FeeFromOutputMsg/PayFeeFromModule kept for Phase-2 executors.
- Authorization keyed by SHA-256 of the msg's proto bytes.
- Value bases derived + memoized (not stored in the asset registry).
- Min 2 actions per bundle (constant), max param 2..32; block cap counts actions;
  max_private_actions_per_block >= 2 x max_actions_per_bundle.
