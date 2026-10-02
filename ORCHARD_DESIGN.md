# Orchard-style shielded bundles for Earth (spike, 2026-10-02)

> **Phase 1 is in production code** on `privacy/orchard` (chain and mobile).
> Where it departs from the spike below, section 12 wins.

Replaces the fixed 3-in/3-out `transfer` circuit with N per-action proofs, a
public value balance per asset, and a binding signature. Pre-genesis: no
migration of state, only of code and formats.

Spike branches (not pushed): chain `spike/orchard` (`zk/orchard`,
`tools/orchardfixtures`, `scripts/orchard-bundles.sh`,
`zk/ultrahonk/orchard_test.go`), mobile `spike/orchard`
(`circuits/action`, `circuits/privacy_core/src/value.nr`).

## 1. Decisions

| Question | Decision | Why |
| --- | --- | --- |
| Assets per action | **Spend and output may differ** (`cv = v_s·G(a_s) − v_o·G(a_o) + rcv·R`) | Costs 187 gates over single-asset (7,933 → 8,120, same 2^13 size). Bundles need max(#spends, #outputs) actions instead of Σ_assets max(...); the common "send A + ERTH fee" fits 3 actions either way, fee-only fits 1. Soundness does not depend on it: each term uses its own asset's canonical base. |
| Canonical value base | **In-circuit hash-to-curve** with a canonical sign of y | 2 bases cost ~190 gates each (plus one shared sign-check gadget). A generator registry tree (depth 16, two paths) measured 10,552 gates, pushing the circuit to 2^14 (~1.6x prove time), adds a public root, chain state and a root window. Hash-to-curve is stateless and cheaper. |
| Counter minimality | Not enforced in-circuit | Each (asset, ctr) is an independent oracle point; a non-least ctr gives the prover a base that only balances against itself. See section 5. |
| Binding signature | Schnorr over Grumpkin, base R, Poseidon2 challenge | Same curve the circuit uses, no new hash for clients. |
| Sighash in proof | **Yes**, each action binds `sighash` | Free (one public input). Proofs cannot be lifted into another tx even if a wallet leaks its rcvs; membership/passport proofs bind the same value. |
| Public balance sign | **Only positive** (value leaving the pool) | Value enters through `MintNote`/`MsgShield` as today. Keeps the release accounting a plain map. |
| Anchor | One per bundle | As Orchard. The stake vote carries two bundles (section 7). |

## 2. Curve and bases

Grumpkin = Noir's embedded curve: `y² = x³ − 17` over the BN254 scalar field
`p` (the circuit Field), prime order `n` = BN254 base modulus, cofactor 1.
Go uses `gnark-crypto/ecc/grumpkin` (v0.20.1 is already a chain dependency);
its generator is Noir's `(1, sqrt(−16))` (tested). Infinity is `(0, 0)` on
both sides.

    hash_to_point(tag, input):
        for ctr = 0, 1, ...:
            x = Poseidon2(tag, input, ctr)
            if x³ − 17 is a square in F_p:
                y = sqrt(x³ − 17), negated if y > (p−1)/2
                return (x, y)

    TAG_GEN  = "earth.gen"   (0x65617274682e67656e)
    TAG_CV_R = "earth.cv.r"  (0x65617274682e63762e72)
    G_a = hash_to_point(TAG_GEN, asset_id)        asset_id = zk/privacy.AssetID(denom), unchanged
    R   = hash_to_point(TAG_CV_R, 0)              ctr 0:
          x = 0x07fc551d28471de4eb62cf956996e963f8a0877bb97afd963ad5f296973401ce
          y = 0x17bbea25d2f097a980174168aaffe0d61e97da2edeb3eec90c3a5e8a3b9328a4
    G_uerth: ctr 0, x = 0x16cbb7…eae68;  G_uanml: ctr 3, x = 0x18a0d7…587a5

The chain and wallets use the least ctr. The circuit takes `(ctr, y)` as an
in-circuit Brillig hint and constrains `x = H(TAG_GEN, asset, ctr)`,
`y² = x³ − 17`, `y ≤ (p−1)/2`. Noir's default generator is never used.

## 3. Action circuit (`circuits/action`)

Private: `nk, s_asset, s_value: u64, s_rho, s_rcm, s_pos: u32, s_path[32],
o_asset, o_value: u64, o_pc, rcv`.
Public, in order: **`anchor, nf, cm_out, cv_x, cv_y, sighash`**.

    owner_pk = H(TAG_OWNER, nk);  pc = H(TAG_PC, owner_pk, s_rho, s_rcm)
    cm_in    = H(TAG_CM, s_asset, s_value, pc)
    s_value ≠ 0  ⇒  merkle_root(cm_in, s_pos, s_path) == anchor
    nf       = H(TAG_NF, nk, s_rho, s_pos)              always (dummies too)
    cm_out   = H(TAG_CM, o_asset, o_value, o_pc)
    cv       = MSM([G(s_asset), −G(o_asset), R], [s_value, o_value, rcv])
    bind(sighash)

Note, nullifier and commitment formulas are today's, so `MintNote`, note
ciphertexts (v1/v2), self-mint pcs, sync and the indexer's note stream are
unchanged. `rcv` is a Field (< r < n), lifted with `EmbeddedCurveScalar::
from_field`; the bias against uniform mod n is ~2^-127.

**Dummy rules.**
- Dummy spend: `s_value = 0`. The path is not checked, so `anchor`, `s_asset`
  and `s_pos` are free. The wallet uses a fresh random `rho` (and may use a
  throwaway `nk`) so `nf` is unique; the chain records it like any nullifier.
- Dummy output: `o_value = 0`, a random `o_pc`, ciphertext encrypted to a
  throwaway key (as today).
- Neither needs a flag: a value-0 term adds nothing to cv.

Tests: 26 in `action` (positive: same/mixed asset, spend-only, output-only,
both dummy, u64 max, rcv 0 and −1, additivity; negative: −G on the output
and on the spend, another asset's base, an arbitrary y, cv for another
asset, cv under/overstating either value, wrong rcv, inflated spend, spend
asset swapped, wrong anchor/nk/nullifier, dummy nullifier still bound, u64
overflow). 9 in `privacy_core` incl. Go-pinned R, bases and three point
arithmetic vectors.

Known lint: nargo's "Brillig call isn't properly covered" fires on the two
hint calls. False positive: it fires on a minimal probe (`y_hint(x)` then
`assert(y*y == x³−17)`) too. Every hint output enters a constraint: ctr
feeds the Poseidon2 that defines x, y the curve equation and the sign check.

## 4. Bundle, sighash, binding signature (`zk/orchard`)

    Action   { nf, cm, cv (x‖y, 64 B), ciphertext, proof }
    Balance  { denom → asset_id, value: u64 > 0 }        one per asset
    Bundle   { anchor, actions[1..32], balances[], binding_sig (96 B) }

    bundle_digest = H(TAG_BUNDLE, anchor, N, nf_0, cm_0, cvx_0, cvy_0, Bytes(ct_0), …,
                      M, asset_0, value_0, …)                 TAG_BUNDLE = "earth.bundle"
    sighash       = Signal(msg_type, chain_id, digest(bundle_0), [digest(bundle_1)], msg fields…)

`Signal` is today's `zk/privacy.Signal`, so the msg-type, chain-id and
per-msg field binding carries over. It replaces `SpendSignal`,
`ActionSignal` and `MultiSpendSignal`: the digest already binds every
nullifier and ciphertext.

Binding key and signature:

    bvk = Σ_i cv_i − Σ_a value_a · G_a        (G_a from the asset registry)
    bsk = Σ_i rcv_i  mod n                     (wallet)
    sig = Rn.x ‖ Rn.y ‖ s:   k = SHA-512(bsk ‖ sighash ‖ 32 random B) mod n,  Rn = k·R
                             e = Poseidon2(TAG_BSIG, Rn.x, Rn.y, bvk.x, bvk.y, sighash)
                             s = k + e·bsk mod n                  TAG_BSIG = "earth.bsig"
    verify: Rn on curve and ≠ O, s < n (canonical), bvk ≠ O, s·R == Rn + e·bvk

`bvk = O` is refused: with bsk = 0 anyone could re-sign another sighash.

Chain verification order (all stateless except anchor and nullifier set):
shape (`ValidateBasic`: 1..32 actions, cv on curve, nf distinct, balances
positive and one per asset, 96-byte sig) → anchor in window → nullifiers
unspent → sighash → **binding signature (0.12 ms)** → action proofs (4.3 ms each).

## 5. Soundness

Claim: if every action proof and the binding signature verify, then for
every asset a, Σ spent notes of a = Σ created notes of a + public value_a.

1. **Notes.** A real spend (value ≠ 0) opens a commitment in the tree under
   the anchor with its asset and value. Its nullifier is a function of the
   note (nk, rho, position) so it is spent once: duplicates are refused
   within a bundle (`ValidateBasic`) and across txs (nullifier set). A
   value-0 spend contributes nothing.
2. **Value.** Extracting the action witnesses, each `cv_i = Σ_B α_iB·B +
   rcv_i·R` over bases B = hash_to_point(TAG_GEN, a, c), with `α = +s_value`
   for the spend's base and `−o_value` for the output's. A valid signature
   gives knowledge of bsk with `bvk = bsk·R`, hence
   `Σ_B (Σ_i α_iB − pub_B)·B + (Σ rcv_i − bsk)·R = O`, where `pub_B` is
   value_a for the least-ctr base of each balance and 0 otherwise. The bases
   and R are independent random-oracle points (distinct Poseidon2 inputs,
   distinct tags), so under discrete log every coefficient is 0 mod n. Each
   |Σ α| ≤ 32·2⁶⁴ ≪ n/2 (u64 range checks, `MaxActions = 32`), so they are 0
   over the integers: per base, spent = created + public.
3. **Per base ⇒ per asset.** A base is bound to one asset (the asset is
   hashed into x, and the circuit uses the same asset for the note
   commitment). Summing over a's counters gives per-asset conservation.
   A non-least ctr is harmless: its base carries no public balance and has
   no known relation to anything, so value under it only cancels against
   value under it, of the same asset.
4. **The −G inflation case.** try-and-increment yields two points per
   (asset, ctr): (x, y) and (x, −y) = −G_a, with the *known* relation
   G + (−G) = O. Outputting v under G_a and v under −G_a nets to zero and
   the binding signature verifies (shown in Go:
   `TestNegatedBaseInflation`). The circuit's `y ≤ (p−1)/2` leaves exactly
   one of the two (tests `test_negated_output_base_rejected`,
   `test_negated_spend_base_rejected`). **The chain cannot detect this; the
   circuit check is the only defence and must be in the audit scope.**
   Other cheap relations: none. Grumpkin has prime order (no torsion, no
   small-subgroup points), and the curve is j=0 so the GLV endomorphism
   (x, y) → (βx, y) maps G_a to λ·G_a (y, and so canonicality, preserved).
   A base equal to another base's image needs Poseidon2 outputs with
   x' = β·x or β²·x: a targeted preimage, or a birthday search of ~2^127
   hashes over many assets/counters, the same as Grumpkin's own security
   level. Acceptable; noted for the audit.
5. **Non-malleability.** Every action proof binds sighash, which binds every
   field of every bundle and the msg. A relayer can neither re-sign (needs
   bsk) nor move proofs (sighash changes). Dropping or adding actions breaks
   the binding key.
6. **Faerie gold.** nf includes the position, so two notes with the same rho
   still have distinct nullifiers (unchanged).

Audit items beyond the circuit: bb's MSM black box (`cycle_group`
batch_mul) must be sound for witness points, including `G_s == G_o` and
`v = 0`; gnark-crypto grumpkin is "partially audited"; the Schnorr is
textbook.

## 6. Fees, unshields, chain-minted notes

- **Release map.** A msg's bundles release `Σ balances` per denom from the
  pool. Each msg declares where every unit goes, and the ante checks the map
  is spent exactly, nothing left over:
  - `fee` (uerth) → fee_collector (then x/earth burns half, unchanged);
  - MsgTransfer: `receiver` gets its denom's remainder (unshield);
  - x/shieldedstaking, x/dex: the action's module takes its denom's
    remainder (`SpendToModule` becomes `Release(denom, amount, module)`).
  `fee_from_output` stays: the ERTH balance is then 0 and the action pays.
- **Fee floor.** Unchanged (`min_fee`, min gas price × gas). Gas becomes
  per action: `base + N·(proof_verification_gas + 2·note_gas)`. The block
  cap should count actions, not txs (`max_private_actions_per_block`).
- **Chain-minted notes** (`MintNote`: registration reward, ANML claims, swap
  outputs, derth/unbond mints, stake-vote re-mints, LP refunds, gas grant)
  have no cv: their value is public, they enter through the turnstile and
  go straight into the tree. Spending one later is an ordinary action. No
  bundle ever creates value, so negative balances are not needed.
- **Turnstile.** Unchanged: `ShieldedIn − ShieldedOut == module balance`.
  Releases are `countOut` per balance.
- **Asset registry.** `RegisterAsset` additionally stores G_a (64 B),
  computed once with `orchard.ValueBase`; bvk reads it.

## 7. Msgs: what changes

`Transfer` is replaced by `Bundle` in every msg; structure otherwise kept.

| Module | Msgs | Change |
| --- | --- | --- |
| x/shielded | MsgTransfer | `bundle`, `receiver`, `fee`, `fee_from_output`; ante: bundle checks, release map. `TransferArity`, `PublicInputs`, `MultiTransferMsg` go; `Transfer` proto, `transfer` VK → `action` VK. |
| x/personhood | MsgRegister, MsgClaimAnml, MsgSetCaretaker, MsgBindReferrer | `fee` Transfer → fee bundle; passport/membership proofs bind `sighash` instead of `ActionSignal`. |
| x/assembly | MsgVoteProposal, MsgProposeRemoval, MsgVoteRemoval | same as personhood. |
| x/shieldedstaking | Delegate, Undelegate, ClaimUnbonding, LockPosition, Update/UnlockPosition, PositionVote | bundle; `Transfer.ValueOut` → the msg's denom balance. |
| x/shieldedstaking | **MsgStakeVote** | **two bundles**: the vote bundle (anchor = proposal snapshot root, balance = derth/v weight only, no fee) and a fee bundle (current anchor). One anchor per bundle keeps "derth spent against the snapshot" checkable even though assets are hidden per action. |
| x/dex | MsgNoteSwap | bundle, balance = asset in. |
| x/dex | MsgAddLiquidityShielded | **one bundle** with ANML and ERTH balances (was two transfers). |

`PrivateActionHandler`, `PrivateAnchorAcceptor` (now per bundle),
`PrivateActionExecutor`, authorization-by-nullifiers and the unsigned-tx ante
route stay as they are.

## 8. Measurements (Apple M2, 8 cores; bb v5.0.0, nargo 1.0.0-beta.22)

Circuit sizes (`bb gates`, noir-recursive):

| Circuit | Gates | Dyadic |
| --- | --- | --- |
| transfer (today, 3-in/3-out) | 12,245 | 2^14 |
| **action** (mixed assets, h2c) | **8,120** | 2^13 |
| action, single asset per action | 7,933 | 2^13 |
| action, generator tree depth 16 instead of h2c | 10,552 | 2^14 |
| action without cv (Merkle + nf + cm only) | 6,000 | |
| 2 actions in one proof (option, section 9) | 13,385 | 2^14 |

8,120 leaves 72 gates of headroom under 2^13; anything added doubles prove
time.

Prove (`bb prove`, wall clock incl. process start), per proof:

| | 1 thread | 8 threads |
| --- | --- | --- |
| transfer (today) | 0.53 s | 0.20 s |
| action | 0.34 s | 0.16 s |

Bundles end to end (Go builds the witness and the binding sig; nargo
execute; bb proves each action; Go verifies):

| Actions | witness (nargo CLI) | prove 1 thr | prove 8 thr | Go verify, serial | Go verify, goroutine per action | binding check |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 0.18 s | 0.34 s | 0.16 s | 4.3 ms | 4.7 ms | 0.12 ms |
| 3 | 0.55 s | 1.05 s | 0.48 s | 12.6 ms | 5.4 ms | 0.12 ms |
| 10 | 1.84 s | 3.43 s | 1.65 s | 41.3 ms | 11.8 ms | 0.14 ms |

Per-action Go verify: 4.35 ms (transfer: 4.0 ms). Proof 14,656 B per
action. All public inputs matched Go's byte for byte (cv, sighash included),
which is the Go↔Noir cross-check of the bases, R and the MSM.

Typical txs vs today:

| Tx | today | Orchard | prove 1 thr |
| --- | --- | --- | --- |
| fee-only (claim, vote, caretaker) | 1 transfer | 1 action | 0.53 → 0.34 s |
| ERTH send + fee from same notes | 1 transfer | 2 actions (pay, change) | 0.53 → 0.68 s |
| ANML send + ERTH fee | 1 transfer | 3 actions | 0.53 → 1.02 s |
| spend 5 notes | impossible (max 3) | 5 actions | 1.7 s |

## 9. Risks and open questions

1. **Prover and verifier cost scale with actions.** A 3-action tx is ~2x
   today's prove time and 3x the CheckTx verify and bytes (44 KB). Options:
   (a) an `action2` VK proving two actions per proof (13,385 gates, 2^14:
   ~0.53 s ST for 2 actions vs 0.68 s; halves proofs and verify), bundles
   may mix both; (b) verify a tx's proofs on goroutines (measured: 10
   actions 41 → 12 ms); (c) price gas per action and cap actions per block.
   Phone prove times still unmeasured (Phase 0 open item; expect ~3-5x).
2. **Action count leaks shape** (max of spends and outputs). Today every
   transfer is the same shape. Wallet policy: pad to at least 2 actions,
   perhaps to buckets (2, 4, 8). Padding costs proofs.
3. **The −G check is the whole defence against inflation** (section 5.4);
   the chain cannot see a violation. Same class as Zcash's ZSA asset-base
   issue. Must be in the circuit audit with an explicit test.
4. **bb MSM black box soundness** for witness bases (equal bases, zero
   scalars) is assumed; ask the auditor or Aztec. Two hint calls trip a
   nargo lint (false positive, section 3).
5. **New crypto in clients.** Android and iOS need Grumpkin add/mul,
   hash-to-curve (Tonelli-Shanks over F_p, p−1 = 2^28·q) and the Schnorr
   signer to compute cv before proving (cv is in the sighash) and to sign.
   ~300 lines each with vectors from `zk/orchard`.
6. **Gate headroom.** 72 gates under 2^13. Fine for this circuit as
   specified; any extension (e.g. enable flags, asset-type public input)
   crosses into 2^14.
7. **Anchor per bundle** forces MsgStakeVote to two bundles (two binding
   sigs). Alternative: per-action anchors, rejected because the chain
   cannot tell which hidden-asset action spends derth.
8. **gnark-crypto grumpkin** is "partially audited", non-constant-time. Chain
   side only verifies (no secrets); wallets must not use it for signing
   with secrets on shared hardware without care (they will use their own).

## 10. Migration effort (to audit-ready, one engineer)

| Item | Weeks |
| --- | --- |
| Circuit: finalize action (+ optional action2), fixtures, VK in genesis, privacy-vks.sh | 1 |
| zk/orchard hardening, proto (`Action`, `Bundle`, `ValueBalance`), asset registry stores G_a | 1 |
| x/shielded ante + keeper: bundle check/verify/execute, release map, per-action gas, action cap, authorization by bundle, fee_from_output | 2 |
| Modules: personhood (4 msgs), assembly (3), shieldedstaking (8, stake vote two bundles), dex (2); signals → sighash; tests, testutil scenario, shielded/staking/dex fixtures | 2.5 |
| Android: BundlePlan (replaces TransferPlan note selection), witnesses, Grumpkin/h2c/Schnorr, multi-proof prover, PrivateMsgs/TxEngine, golden vectors, WalletFlowTest | 2.5 |
| iOS port of the same | 2 |
| Web app proto regen (read-only), indexer (actions stream: nf/cm/ct unchanged), backend vectors | 0.5 |
| **Total** | **~11.5 weeks** |

Audit scope changes: `transfer` → `action` circuit plus the binding
signature, value-base canonicality and the bundle release accounting.

## 11. Reproduce

    # mobile privacy/orchard
    cd circuits && nargo test --package action && nargo test --package privacy_core
    # chain privacy/orchard
    go test ./zk/orchard/ ./x/shielded/... ./app
    ./scripts/privacy-vks.sh <mobile>/circuits         # action + membership keys
    ./scripts/shielded-fixtures.sh <mobile>/circuits   # x/shielded scenario proofs
    NARGO=nargo BB=bb ./scripts/orchard-bundles.sh <mobile checkout> 1 2 3 10
    go test ./zk/ultrahonk/ -run TestOrchard -bench Orchard

## 12. Phase 1 (production) deltas, 2026-10-02

| Spike | Production (x/shielded, zk/orchard) |
| --- | --- |
| One anchor per bundle | **One anchor per action** (`Action.anchor`), every one checked against the window, dummies included. Bundle 0's actions may be vouched for by the msg's `PrivateAnchorAcceptor` (stake votes); other bundles must be in the window. |
| `digest = H(TAG_BUNDLE, anchor, N, ...)` | `digest = H(TAG_BUNDLE, N, [anchor_i, nf_i, cm_i, cvx_i, cvy_i, Bytes(ct_i)]..., M, [asset_j, value_j]...)` |
| `sighash = Signal(type, chain, D_0, ...)` | `sighash = Signal(type URL, chain id, K, D_0..D_{K-1}, msg fields)`; K (bundle count) keeps a digest from posing as a field. MsgSend's fields: `Bytes(receiver raw address), fee`. |
| `MsgTransfer{bundle, receiver, fee, fee_from_output}` | `MsgSend{bundle, receiver, fee}`. Release map: the uerth balance pays `fee` to fee_collector; every remainder (all denoms) goes to `receiver`, paid **in the ante** (atomic with the spend); no receiver ⇔ balances == fee. An unshield of uerth pays its fee from what it releases, with no fee note. `FeeFromOutputMsg` + `PayFeeFromModule` stay for Phase 2 executors (claims, swaps). |
| Asset registry stores G_a | Bases derived (`orchard.ValueBase`, memoized); only registered denoms may carry a balance. |
| Balance per bundle | Balances `{denom, amount}`, positive, one per denom, at most 2 per action. |
| `1..32` actions | **2..32**: `MinActionsPerBundle = 2` (padding, stateless), param `max_actions_per_bundle` (2..32, default 16). |
| Gas `base + N·(proof + 2·note)` | `bundle_gas` (100,000) per bundle + `proof_verification_gas + 2·note_gas` per action, charged before any work. `max_private_actions_per_block` (default 32, ≥ 2 × max per bundle) counts actions. |
| Verification order | shape → per-bundle size cap → gas → block cap → anchors → nullifiers → assets → capacity → release map → sighash → **every binding signature** → **every action proof in parallel** (`orchard.VerifyProofs`, first failure in bundle/action order, deterministic). |
| Authorization keyed by nullifiers | Keyed by SHA-256 of the msg's proto bytes; `AuthorizedAction/Result/Positions(ctx, msg)`, `ReleaseToModule(ctx, msg, denom, module)` (whole remainder, once). |

Circuit unchanged in size (8,120 gates); 38 tests. The transfer circuit, its
key and fixtures are gone; Phase 2 modules (personhood, assembly,
shieldedstaking, dex) still carry a legacy `Transfer` and report no bundle,
so the ante refuses their private msgs until they are ported
(`TODO(orchard-phase2)`).
