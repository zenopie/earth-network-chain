# Audit round 5: chain fixes

Branch privacy/orchard from 4a663d5. Report: audit5-chain.md (scratchpad).
Fresh-genesis relaunch: no migrations. Wallet-facing rules:
ORCHARD_DESIGN.md section 16.

- [x] A1 (H3) allocation: restoreStream adds an INTEGRATED option to
  IntegratedOptions only if not Removed; pruneOption removes the
  IntegratedOptions entry; resolveIntegrated drops a dangling entry on
  ErrNotFound. Tests: x/allocation/keeper/audit5_struck_integrated_test.go
  (export -> import -> 30+ days of BeginBlock).
- [x] P1 (H1) referral note: the chain mints it to the handle's owner_pk,
  rho/rcm = H("earth.referral", passport nullifier, leaf index, 0|1)
  (privacy.ReferralOpening), no ciphertext; the opening is on the
  shielded_mint event (owner_pk, rho, rcm). MsgRegister affiliate_pc /
  affiliate_ciphertext removed (11, 12 reserved); affiliate field =
  H("earth.affiliate", Bytes(handle)). register event: handle, referral,
  referral_position. A handle that stopped resolving within the block
  lands the registration unreferred. x/shielded MintOpenNote. Tests:
  app TestPrivatePersonhood (requireReferral), personhood unit tests.
- [x] D1 (H2) dex: MintNoteSplit (ceil(v/(2^64-1)) notes, at most 64) for
  LP payout legs; a failed payout is kept with escrowed shares and retried
  at now + 1h << min(attempts-1, 8) (LpUnbonding.payout_attempts), never
  dropped; per-sweep note budget 256; a withdrawal whose note leg exceeds
  16 notes' worth is refused at start. Other mint paths checked: swaps,
  deposits, refunds, BuyAnml, unbond claims, undelegate, shield are atomic
  (a > u64 value fails the tx, nothing lost); registration and referral
  halves are 1e-4 of an option (would need 1.8e23 uerth); ANML claim is 1
  ANML; change outputs are circuit u64. Tests:
  x/dex/keeper/audit5_payout_test.go.
- [x] P2 (M1) handles: the claim bound applies unless the prover holds a
  live handle (renewal period = bounded); MsgMoveHandle refuses a handle
  not live; caretaker: a lapsed unswept split is not held. Tests:
  x/personhood/keeper/audit5_handles_test.go.
- [x] P3 (M2) Query/LeaseBounds: effective handle lease (max ever),
  caretaker lease (with LeaseHold), margin, and both bounds at block time.
- [x] Lows fixed: L-P5 (bind gas 9 writes), L-SH1 (anchor margin 120 s in
  CheckTx/ReCheckTx, PrepareProposal drops lapsed anchors), L-SH2 (Shield
  checks SendEnabled), L-SH3 (cap doc: bundle actions only), L-DX3 (TWAP
  export), L-DX4 (fee ceil), L-ST1 (checkpoint walk stops early), L-ST2
  (validate first, derth u64), L-AS1 (gov-demoted round = new scope),
  L-AS2 (chamber votes pass the breaker; gas params capped), L-AS3
  (ProposalTally bar), L-AS4 (stale ballots swept with a cursor; not
  imported).
- Lows skipped: L-P4 (inherent: the handle must be resolvable, and the
  referral recipient and amount are public by design); L-P6 (design:
  commit-reveal only if names gain value); L-DX2 (not cheap: needs a
  batched cursor or a pool-creation fee, a design choice); L-SH3's
  "max-shape fixed gas <= consensus max_gas" check (max_gas is a consensus
  param Params.Validate cannot see; a too-large shape only makes the
  largest txs unincludable; the gas caps bound the chamber case). Info:
  AuthorizedNullifiers left (tests use it).
- [x] VKs: privacy-vks.sh --check: unchanged (no circuit change).
- [x] fixtures: personhood (passports + app: personhood-fixtures.sh all),
  dex (dex-fixtures.sh). Shielded, staking and orchard fixtures unchanged
  (their tests pass as committed).
- [x] genesis regenerated: sha256
  67a6ed4af4a6478852dd9a843ea8a26dd6038ed1c81b09a1ce9c7636823f3212
  (build-genesis.sh --check: up to date).
- [x] go test ./... passes; go vet clean.
