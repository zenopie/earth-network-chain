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
o_asset, o_value: u64, o_pc, rcv`; both values at most 2^63-1 (section 16).
Public, in order: **`anchor, nf, cm_out, cv_x, cv_y, sighash`**.

    owner_pk = H(TAG_OWNER, nk);  pc = H(TAG_PC, owner_pk, s_rho, s_rcm)
    s_value, o_value < 2^63                             (note_cm, section 16)
    cm_in    = H(TAG_CM, s_asset, s_value, pc)
    s_value ≠ 0  ⇒  merkle_root(cm_in, s_pos, s_path) == anchor
    nf       = H(TAG_NF, nk, s_rho, s_pos)              always (dummies too)
    cm_out   = H(TAG_CM, o_asset, o_value, o_pc)
    cv       = MSM([G(s_asset), −G(o_asset), R], [s_value, o_value, rcv])
    bind(sighash)

Note, nullifier and commitment formulas are today's, so `MintNote`, note
ciphertexts (v1/v2), sync and the indexer's note stream are unchanged.
(Self-mint pcs are retired 2026-10-02: every minted note now carries a
blind ciphertext, §14.) `rcv` is a Field (< r < n), lifted with `EmbeddedCurveScalar::
from_field`; the bias against uniform mod n is ~2^-127.

**Dummy rules.**
- Dummy spend: `s_value = 0`. The path is not checked, so `anchor`, `s_asset`
  and `s_pos` are free. The wallet uses a fresh random `rho` (and may use a
  throwaway `nk`) so `nf` is unique; the chain records it like any nullifier.
- Dummy output: `o_value = 0`, a random `o_pc`, ciphertext encrypted to a
  throwaway key (as today).
- Neither needs a flag: a value-0 term adds nothing to cv.

Tests: 26 in `action`, 41 today (positive: same/mixed asset, spend-only,
output-only, both dummy, 2^63-1 max (u64 max before section 16), rcv 0 and −1, additivity; negative: −G on the output
and on the spend, another asset's base, an arbitrary y, cv for another
asset, cv under/overstating either value, wrong rcv, inflated spend, spend
asset swapped, wrong anchor/nk/nullifier, dummy nullifier still bound, u64
overflow, an output of 2^63 or 2^64-1 and a spend of 2^63). 9 in
`privacy_core` (12 today) incl. Go-pinned R, bases and three point
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
    sighash       = Signal(msg_type, chain_id, K, digest(bundle_0), …, digest(bundle_K-1),
                           Bytes(memo), timeout_height, gas_limit, msg fields…)

(Superseded 2026-10-02, see §14: the tx body's memo and timeout_height and
the auth info's gas_limit are bound after the digests.)

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
   |Σ α| ≤ 32·2⁶³ ≪ n/2 (63-bit range checks, `MaxActions = 32`), so they are 0
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
  outputs, derth/unbond mints, LP refunds, gas grant)
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
| x/personhood | MsgRegister, MsgClaimAnml, MsgSetCaretaker, MsgBindHandle | `fee` Transfer → fee bundle; passport/membership proofs bind `sighash` instead of `ActionSignal`. |
| x/assembly | MsgVoteProposal, MsgProposeRemoval, MsgVoteRemoval | same as personhood. |
| x/shieldedstaking | Delegate, Undelegate, ClaimUnbonding, LockPosition, Update/UnlockPosition, PositionVote | bundle; `Transfer.ValueOut` → the msg's denom balance. |
| x/shieldedstaking | **MsgStakeVote** | **two bundles**: the vote bundle (anchor = proposal snapshot root, balance = derth/v weight only, no fee) and a fee bundle (current anchor). One anchor per bundle keeps "derth spent against the snapshot" checkable even though assets are hidden per action. |
| x/dex | MsgNoteSwap | bundle, balance = asset in. |
| x/dex | MsgAddLiquidityShielded | **one bundle** with ANML and ERTH balances (was two transfers). |

`PrivateActionHandler`, `PrivateActionExecutor`, authorization-by-nullifiers and the unsigned-tx ante
route stay as they are.

## 8. Measurements (Apple M2, 8 cores; bb v5.0.0, nargo 1.0.0-beta.22)

Circuit sizes (`bb gates`, noir-recursive):

| Circuit | Gates | Dyadic |
| --- | --- | --- |
| transfer (today, 3-in/3-out) | 12,245 | 2^14 |
| **action** (mixed assets, h2c) | **8,120** (8,098 with the 2^63-1 note bound, section 16) | 2^13 |
| action, single asset per action | 7,933 | 2^13 |
| action, generator tree depth 16 instead of h2c | 10,552 | 2^14 |
| action without cv (Merkle + nf + cm only) | 6,000 | |
| 2 actions in one proof (option, section 9) | 13,385 | 2^14 |

8,120 left 72 gates of headroom under 2^13 (8,098 now leaves 94); anything
added doubles prove time.

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
| One anchor per bundle | **One anchor per action** (`Action.anchor`), every one checked against the window, dummies included. (The `PrivateAnchorAcceptor` hook for out-of-window stake-vote anchors had no implementer and was removed.) |
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

## 13. Phase 2 (2026-10-02): modules on bundles, private LP, stake notes

**Fees.** (Superseded 2026-10-02 by the one fee rule, §14: no staking or
dex msg has a `fee` field any more.) Personhood and assembly msgs carry
`Bundle fee = 1`; its only balance is the uerth fee. Staking and dex msgs
release their value as the release-map remainder of their denom. Membership
proofs bind the msg's sighash as their signal (`SignalOf = Sighash`). A msg
paying its whole fee from its output (an unbonding claim) may carry no
bundle at all.

**Dex, private LP shares (user decision).** `dexlp/<pool>` is a pool asset
(admitted on first use); MsgAddLiquidityShielded mints the shares as a note
to `share_pc`; MsgRemoveLiquidityShielded releases exactly `dexlp/<pool>`
into an LpUnbonding with no address (`withdrawal_id = 0x00 || first nf`),
paying both legs as notes at maturity. `dexlp/*` notes cannot be unshielded
(`RegisterPoolLockedPrefix`). Only module-held liquidity is public.

**Private staking: owner-locked stake notes (user decision).** derth is
non-transferable. `derth/<valoper>` and `unbond/<valoper>/<epoch>` are not
coins and not pool assets (`ExcludeAssetPrefix`; shielded-only for every
transparent path, so the dex refuses them): they are notes of
x/shieldedstaking's own append-only depth-32 Poseidon2 stake tree, with its
own nullifier set, root window (`stake_root_window_seconds`, 14 days) and
proposal snapshot roots. Supply is a book entry
(`ValidatorState.derth_supply`).

    spc  = H(TAG_SPC, owner_pk, rho, rcm)        owner_pk = H(TAG_OWNER, nk)
    cm   = H(TAG_STAKE, AssetID(denom), amount, spc)
    nf   = H(TAG_SNF, nk, rho, position)
    otag = H(TAG_OTAG, owner_pk, salt)
    TAG_STAKE "earth.stake", TAG_SPC "earth.spc", TAG_SNF "earth.snf", TAG_OTAG "earth.otag"

Circuit `stake` (mobile 2253c48, 9,647 gates, 2^14, 19 tests; 9,672 with
the 2^63-1 note bound on every input and output, section 16): up to two
inputs under `anchor` (amount 0 = none: nf 0, no path), up to two outputs
(amount 0 = none: cm 0), one asset, `in0 + in1 + v_in == out0 + out1 +
v_out`, and `spc_mint`/`otag` of the SAME owner_pk; binds the sighash.
Public: `anchor, asset, nf_0, nf_1, cm_out_0, cm_out_1, v_in, v_out,
spc_mint, otag, sighash`. The chain fixes asset (the msg's stake denom, 0
for position msgs), v_in (0) and v_out (the msg's amount). Every staking
msg's sighash binds the StakeFields first: `anchor, nf_0, nf_1, cm_0, cm_1,
Bytes(ct_0), Bytes(ct_1), spc_mint, otag`.

| Msg | proof | chain |
| --- | --- | --- |
| Delegate {bundle: ERTH + fee} | spends/creates nothing | mints derth at the live rate to spc_mint |
| Restake (merge/split) | spends 1-2, creates 1-2 | — |
| Undelegate {amount} | spends, v_out = amount, change | mints owner-locked unbond claim (value at rate) to spc_mint (superseded 2026-10-03, §18: queues a payout to a pool pc, minted at maturity by the chain) |
| ClaimUnbonding {amount, pc, fee or fee_from_output} | spends claims, v_out = amount | mints ERTH (transferable) to pc in the pool; no bundle with fee_from_output (retired 2026-10-03, §18) |
| StakeVote {weight} | (superseded 2026-10-03, §15: vote proof, circuits/vote; nothing spent; §18: up to four notes, one weight) | records vote |
| LockPosition {amount} | spends, v_out = amount, change; otag | position stores otag |
| Update/Unlock/PositionVote | spends nothing; otag must equal the position's | unlock mints derth to spc_mint |
| Redelegate {src, dst, amount} (2026-10-03, §19) | spends derth/src, v_out = amount, change | moves the value src -> dst, mints derth/dst at dst's live rate to spc_mint |

Owner proof replaces the one-time position key and nonce (the sighash binds
the fee bundle, so a proof is never reusable). Rates, epochs, slashing,
MaxEntries, gov tally, Groundworks weight and self-bond compounding are
unchanged.

**Groundworks positions are weighed per validator (2026-10-02, user
decision).** A position no longer is its own Groundworks voter. x/shieldedstaking
keeps, per (validator v, option o), `T[v][o] = sum over v's live positions
of derth x percent` (exact integers, `GwTotals`); Lock, Update and Unlock
add or take off exactly the position's own `derth x percent` terms, so
totals return to zero when the positions go. All of v's positions are ONE
weighted voter in x/allocation, key `"gwpos/" || val_bytes` (26 or 38
bytes, never an account's 20/32 or a nullifier's 32), with an absolute
weight per option `trunc(epoch_rate_v x T[v][o] / 100)`
(`Voter.option_weights`, `allocation.SetWeightedVoter`). The epoch end, a
book processed later in the sweep, and a slash re-file one voter per
validator: O(validators), never O(positions). So positions are uncapped
(`max_positions` and the position count are gone) and `min_position` is
back to 1 ERTH. Versus voting each position on its own, an option gets at
least as much and at most 2 uerth-weight more per position (one truncation
instead of two). A position's `weight` is no longer stored; queries fill in
derth x epoch rate while its split is live. A governance reset of the
Groundworks stream is honoured lazily: each position records the stream
epoch its split was cast in (`split_epoch`) and each validator's totals
theirs (`GwEpoch`); stale ones count as zero (dropped at the next touch or
epoch end) and the owner re-votes with MsgUpdatePosition. InitGenesis
rebuilds the totals from the positions; invariant 6 checks them. Position
votes on x/gov proposals (stake-tree snapshot) and the self-bond weight path
are unchanged.

**Validator income compounds; the only exit is unbonding.** Each
validator has a REWARD ESCROW, `types.RewardEscrowAddress(val) =
address.Module("shieldedstaking", "reward_escrow", val)` (32 bytes, no key,
not on the blocked list), and the chain sets the operator's x/distribution
withdraw address to it (AfterValidatorCreated, before MsgCreateValidator's
self-delegation; InitGenesis for every validator; distribution's store
setter, bypassing `withdraw_addr_enabled=false`). So everything
distribution pays the operator — explicit withdrawals, and the self-bond
rewards its delegation hook pays on every self-bond change (operator
MsgDelegate/MsgUndelegate: a daily 1uerth self-delegation harvests nothing)
and the commission (WithdrawValidatorCommission and the force-withdraw at
validator removal both pay the withdraw address) — lands in the escrow.
The bank send restriction seals it: coins in only from x/distribution, out
only to its own operator (moved by the module; `RewardEscrows` maps escrow ->
validator). At each epoch end every active (bonded, unjailed) validator's
self-bond rewards and commission are withdrawn to the escrow, and the
escrow's whole uerth balance moves to the operator account and is
self-delegated in the same guarded cache context: never liquid. Jailed (or
unbonded) validators skip; their escrow keeps accruing until active again.
When x/staking removes a validator (operator unbonded the whole self-bond,
the 21-day unbonding passed, no delegations left), AfterValidatorRemoved
releases the escrow — every denom — to the operator, forgets it and resets
the withdraw address. Non-uerth rewards stay in the escrow until then.
Escrows are deterministic from the validators: not exported, rebuilt at
InitGenesis (which refuses an operator whose withdraw address is neither
itself nor its escrow; `genesis validate` likewise); invariant 5 checks
every validator has its escrow recorded and as withdraw address, and no
other escrow is recorded. Defense in depth, unchanged routes: operator
MsgWithdrawDelegatorReward, every MsgWithdrawValidatorCommission, and
MsgSetWithdrawAddress (anyone while disabled; with withdraw addresses
re-enabled by gov, an operator to anything but its escrow — itself
included — and anyone to a validator's escrow) are refused by the ante (top
level, authz MsgExec) and by app/operator_router.go, the message router
authz dispatch, gov and group execution, the ICA host and contracts use;
compounding resets an operator's withdraw address found pointing elsewhere.
Liquid validator income would back a staking-derivative scheme, so an
operator's only way to take rewards or commission out is to unbond its
self-bond. See x/shieldedstaking/keeper/escrow.go, withdraw_addr.go.

**Future option (noted for the user).** If selling of whole accounts
(mnemonics) appears, stake notes can additionally be bound to a personhood
identity: the stake circuit would also prove an identity leaf (or bind
owner_pk to an idc), so stake could not outlive a transfer of the account.

## 14. Audit fixes and wallet-facing rules (2026-10-02)

From the x/shielded audit (M1, M2, L1 to L5, info), plus two rules decided
with the user. Everything here changes consensus and wallet formats; every
proof fixture was re-recorded.

**Sighash binds the tx fields (M1).** A private tx is unsigned, so its
relayer could rewrite whatever lies outside the msg: the memo (an
exchange's deposit tag), timeout_height (strip the wallet's expiry) and
gas_limit (lower it so the handler runs out of gas after the ante spent
the notes). Every private msg's sighash is now

    sighash = H(TAG_SIGNAL, Bytes(msg_type_url), Bytes(chain_id),
                K, digest(bundle_0), …, digest(bundle_K-1),
                Bytes(memo), timeout_height, gas_limit,
                msg fields…)

`Bytes(memo)` over the memo's UTF-8 bytes (Bytes("") for none),
timeout_height and gas_limit as u64 field elements, both 0 when unset.
`zk/orchard.Sighash(msgType, chainID, TxFields{Memo, TimeoutHeight,
GasLimit}, bundles, fields…)`; the private ante records the fields
(`types.WithTxFields`) and everything computes the sighash with
`types.SighashOf(ctx, msg, ac)`. A wallet picks the gas limit (simulate:
proofs are not verified there) and memo before proving. `timeout_timestamp`
is refused for private txs (unordered already was). Nothing left in the tx
bytes can change without changing the sighash.

**Exact proof length (M1).** bb v5.0.0 UltraHonk ZK proofs are 458 field
elements (14,656 bytes) for every circuit; bb ignored trailing bytes, so a
proof with 1..31 bytes appended verified (one tx, many hashes).
`zk/ultrahonk.Verify` and every stateless check (`types.ProofBytes`,
`CheckProofLength`: action, stake, membership, passport) require exactly
14,656.

**Canonical bech32.** MsgSend's receiver, personhood's affiliate and
referrer and every staking valoper must be the lowercase canonical encoding
(fix/staking F0 and this branch); no private msg carries any other address.

**Cost of junk (M2, L1).** A tx failing CheckTx pays nothing, and the
binding signature is no filter (anyone can sign a forged balance over
unproven value commitments). CheckTx orders the work cheapest first (shape,
anchors, nullifiers, assets, release map, the action's state checks,
binding signatures) and then verifies proofs one at a time, stopping at the
first failure (`orchard.VerifyProofsSequential`): a junk tx costs a node one
proof verification. FinalizeBlock keeps the parallel, deterministic path.
In a block every proof is paid for first: the fixed private gas charge
(`proof_verification_gas` per action) is consumed before any proof is
verified and counts toward max_gas even when the ante fails, so block gas
bounds a block's verification work (6 forged 16-action txs: 2 reach the
proofs, 4 are refused for block gas). `max_private_actions_per_block` counts
only txs that pass their ante (its count is written with the ante's other
writes). Operators: rate-limit CheckTx per peer on public nodes (sentries in
front of validators, `p2p` connection limits, RPC broadcast limits), as for
any free mempool traffic. bb's per-proof "verification failed" stderr line is
silenced (bb_log_level 3 at start-up; BB_VERBOSE keeps it).

**Pool self-dealing (L2, L3).** MintNote, PayFeeFromModule and
ReleaseToModule refuse the pool's own module as the other side. The pool's
module account has no Minter or Burner permission (it never needed one).

**Ante events (L4).** A private tx's ante writes (nullifiers spent, notes
appended, fee paid, unshield) persist when its msg fails, and so do their
events: a failed tx's result still carries them. Indexers must read note,
nullifier and fee events from failed private txs (the Earth indexer does).

**Release map completeness (L5).** Every PrivateActionHandler declares the
denoms it takes out of the pool (`ReleasedDenoms`); the pool refuses, before
spending anything, a msg whose remainders are not exactly that set. A
delegation takes uerth; a swap its denom_in; an LP deposit both legs; an LP
withdrawal its pool's shares; everything else nothing.

**Priority.** A private tx's priority is fee/gas capped at MaxInt64.

**One note-discovery rule (user decision).** Every note the chain mints to a
hidden owner carries an amount-blind ciphertext supplied by the msg that
asks for it, required, exactly 177 bytes, bound by the msg's sighash (or
signature, for a signed msg) and emitted with the note (note event, and the
mint/shield event): MsgShield (incl. the gas grant), MsgRegister
(ciphertext_anml/erth), MsgClaimAnml, MsgBuyAnml, MsgNoteSwap,
MsgAddLiquidityShielded (share, refund: one ciphertext for both refund
notes), MsgRemoveLiquidityShielded (erth and token legs), MsgRemoveLiquidity
(ANML leg), MsgClaimUnbonding (retired, §18; MsgUndelegate's payout
ciphertext instead). Pool notes use v2
(`EncryptBlindNote`: salt "earth.note.v2", pt 0x02||rho||rcm||memo64);
stake notes the chain mints (Delegate's derth, Undelegate's claim until
§18, UnlockPosition's derth; StakeVote's re-mint until §15) use the new blind stake
ciphertext `StakeProof.spc_ciphertext`:

    ct  = epk || ChaCha20-Poly1305(HKDF-SHA256(X25519(esk, ek_pub),
                 salt "earth.stake.v1", info epk), nonce 0, pt)
    pt  = 0x03 || rho || rcm || memo64                     (177 bytes)

The owner opens it, recomputes spc = H(TAG_SPC, owner_pk, rho, rcm) and
cm = H(TAG_STAKE, AssetID(denom), amount, spc) from the published denom and
amount, and accepts only a matching cm (golden vector
`goldenBlindStakeCT`, Python-cross-checked). `spc_ciphertext` is bound last
in StakeFields: anchor, nf_0, nf_1, cm_0, cm_1, Bytes(ct_0), Bytes(ct_1),
spc_mint, owner_tag, Bytes(spc_ciphertext); it is required exactly when the
msg mints and must be empty otherwise. Wallets drop self-mint counters:
every owned note is found by trial decryption and the cm check.

**One fee rule (user decision).** Every private msg pays its fee out of its
bundles' uerth balance: fee = the uerth balance less the uerth the msg itself
moves (`types.FeeAfter`). The `fee` fields of every staking and dex msg are
gone (reserved). What a msg moves is explicit where it is uerth:
MsgDelegate.amount (new, field 5), MsgNoteSwap.denom_in/amount_in (new,
8/9; the fee is the whole uerth balance unless denom_in is uerth),
MsgAddLiquidityShielded.erth_amount (new, 11). Every other staking and dex
msg (and every personhood and assembly msg) pays its whole uerth balance.
Exceptions, exactly two: MsgSend names its fee (its uerth beyond the fee is
unshielded to its receiver), and MsgClaimUnbonding may instead pay
`fee_from_output` out of the ERTH it claims, with no bundle. MsgNoteSwap's
`fee_from_output` is gone (a holder of only ANML needs an ERTH note to pay a
swap's fee).

## 15. Stake votes without spending (2026-10-03)

**Problem.** MsgStakeVote spent the voting note and re-minted it, so a note
could vote on only one of several concurrently open proposals: a decoy
proposal opened alongside the real one could soak up the votes of whoever
voted on it first. Revealing the note's spend nullifier at each vote instead
(no spend) was rejected: one nullifier on every vote links the votes to each
other and to the note's later spend.

**Design (user approved).** A vote proves its note was unspent at the
snapshot by NON-membership of its spend nullifier in an indexed Merkle tree
of stake nullifiers, and publishes a per-proposal vote nullifier instead.

**Stake nullifier tree** (zk/indexed; x/shieldedstaking/keeper/nf_tree.go).
Replaces the stake nullifier set. An indexed (sorted, Aztec-style) tree on
the same depth-32 Poseidon2 Merkle tree as the note trees:

    leaf_i   = H(TAG_SNFL, value, next_value, next_index)    TAG_SNFL "earth.snfl"
    sentinel = leaf 0 = (0, smallest value, its index); written by the first insert
    empty slot = 0 (no tagged hash equals 0, so it is never a leaf)
    next_value = 0 (next_index 0) for the largest value

Inserting nf (nonzero, not present): low = the leaf with the largest value <
nf (the sentinel if none); append (nf, low.next_value, low.next_index) at the
next index n; set low to (low.value, nf, n). Two O(32) path updates. nf is
absent iff a leaf has value < nf and (nf < next_value or next_value = 0).
Values are canonical field elements compared as integers (32-byte big-endian
encodings sort the same way). Empty-tree root (sentinel alone, size 0 or 1):
`indexed.EmptyRoot` = 0x18f5a2d2d3273f584793e90ac9bf77abf0ff2a05a101cd5943eaf7bbd0bd5b10.
Stored: value -> leaf index (`StakeNullifiers`, the low-leaf lookup), leaf
index -> value (`StakeNfValues`, insertion order), nodes, size (sentinel
included); the root and size recorded at the end of every block that changed
it (`StakeNfLatestRoot/Size`, beside the stake root). No root window: only
snapshots prove against it.

**Snapshot.** `ProposalSnapshot` gains `nf_root`, `nf_size`: the nullifier
tree's latest recorded root and size, taken with the note tree's latest root
(`root`, `tree_size`). Both are as of the end of the last block that changed
them, so they describe one moment. A note under `root` is unspent then iff
its nullifier is absent under `nf_root`.

**Circuit `vote`** (mobile circuits/vote, 9,046 gates, 2^14, 23 tests incl. Go
parity; 9,072 with the 2^63-1 bound on the note's amount, section 16;
privacy_core `nf_leaf`, `vote_nf`, `assert_not_in_indexed`).

    private  nk, amount, rho, rcm, pos (u32), path[32],
             low_value, low_next_value, low_next_index (u32), low_index (u32), low_path[32]
    public   note_root, nf_root, asset, weight (u64), proposal_id (u64), vnf, sighash

    cm  = H(TAG_STAKE, asset, amount, H(TAG_SPC, H(TAG_OWNER, nk), rho, rcm))
    merkle_root(cm, pos, path) == note_root
    nf  = H(TAG_SNF, nk, rho, pos)                       (the note's spend nullifier, private)
    merkle_root(nf_leaf(low_*), low_index, low_path) == nf_root
    low_value < nf;  low_next_value == 0 or nf < low_next_value
    0 < weight <= amount
    vnf = H(TAG_VNF, nk, rho, pos, proposal_id)          TAG_VNF "earth.vnf"
    bind(sighash)

`weight <= amount` (a refinement): a wallet may vote less than the note
holds, e.g. a rounded weight, so the amount need not be published exactly.
Negative tests: a nullifier inserted before the snapshot (by its predecessor,
by its own leaf, by the pre-insertion low leaf), a forged low leaf, an empty
slot as low leaf, a low leaf above nf, wrong proposal id, the spend nullifier
or another position as vnf, weight above amount or zero, another owner's,
validator's or post-snapshot note, a wrong nf root.

**MsgStakeVote** (fields 7 `stake` and response field 1 reserved):

    bundle (fee), proposal_id, validator, options, weight, proof (8), vote_nullifier (9)
    public inputs: snapshot.root, snapshot.nf_root, AssetID(derth/<validator>),
                   weight, proposal_id, vote_nullifier, sighash   (the chain fills all but vnf)
    sighash fields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)),
                    weight, vote_nullifier

The chain refuses: no open snapshot, a snapshot without nf_root, weight above
the validator's snapshot supply, or (proposal, vote_nullifier) already
recorded (ErrVoteNullifierUsed, code 1119: final, no re-vote). It records the
vote under key 0x00 || vnf at the validator, exactly as before (tally
unchanged: weight at the snapshot rate, the module-share caps). Nothing is
spent, nothing minted, no spend nullifier appears. Gas is fixed:
gasVote (250,000) + proof_verification_gas + one note_gas, whatever the tree
sizes. Event `shieldedstaking_stake_vote` carries `vote_nullifier`.

**What this allows and refuses.** One note votes on every open proposal,
each with its own vnf; its votes are unlinkable to each other and to its
later spend (nk is needed to relate them), except by the public weight and
validator. A note spent before the snapshot cannot vote; one minted after
(including a spend's outputs) is not under the note root; one spent after
the snapshot still votes (its nullifier went in after nf_root) and its
outputs cannot: each unit of derth at the snapshot votes at most once per
proposal. A note may now vote after it was undelegated (before, the
undelegation spent it); this is the same diluted voice as "vote, then
undelegate" (audit F6): the tally applies snapshot fractions to the module's
current shares.

**Positions** are unchanged: a position is a public object keyed by id, so
MsgPositionVote never spent anything and already votes on every open
proposal (and may replace its vote). The created-before-the-snapshot-block
rule is exactly consistent with the new snapshot: a position locked in an
earlier block has its notes' nullifiers under nf_root (the notes cannot
vote, the position does); one locked in the snapshot's block or later may
not vote, and its notes can.

**Gas of every other stake msg.** An insert rewrites two paths, so each
stake proof nullifier slot now prices two note writes: +2 x note_gas
(+300,000 at defaults) for every stake msg.

**Genesis.** `stake_nullifiers` is exported in insertion order; InitGenesis
re-inserts them in order and checks every snapshot's nf_root against the
tree at its nf_size (and votes keep their 0x00 || vnf keys). Validate refuses
a zero or repeated nullifier, a malformed nf_root, an nf_size beyond the
tree, a repeated or malformed vote key. Invariant 7 (O(1)): the tree size
matches its last value's leaf index.

**Wallet format (building a vote).**

1. `Query/Snapshot(proposal_id)`: root, tree_size, nf_root, nf_size.
2. The note's path in the stake note tree of the first tree_size leaves (the
   wallet's stake tree, as for any stake proof at an old anchor). A note at
   position >= tree_size cannot vote on this proposal.
3. The nullifier tree at the snapshot: the first nf_size - 1 stake nullifiers
   in insertion order (none if nf_size is 0), inserted in that order into an
   indexed tree (rules above); its root must equal nf_root. Sources: `Query/
   StakeNullifierTree{start, limit}` (values at leaf start+1.., up to 1000 a
   page, with the current size and roots), or the
   `shieldedstaking_stake_nullifier` events, which now carry `index` (the
   leaf index; read failed txs too: a claim spends in the ante).
4. Low leaf of nf = H(TAG_SNF, nk, rho, pos): the predecessor (largest value
   < nf; else the sentinel: value 0, index 0) with next = nf's successor
   (smallest value > nf; else 0, 0); low_index its leaf index, low_path its
   siblings (leaf level first). If nf is in the tree the note was spent
   before the snapshot and cannot vote.
5. vnf = H(TAG_VNF, nk, rho, pos, proposal_id); weight in 1..amount.
6. Prove circuits/vote (Prover.toml names as above), fill the fee bundle,
   sighash as above. Remember (proposal, vnf) as voted; the note is untouched
   (no re-mint, no spc_ciphertext, no stake proof for votes).

Golden vectors (Go `zk/indexed` TestNoirParity = Noir `test_go_parity`):
nf_leaf(1, 2, 3) = 0x0cdc3a81748c6389efaa3a6c29b7f4609a8e9f860230b70413e8bef512978276;
vote_nf(0x5eed, 0xa1, 1, 7) = 0x1ada84dad3e6afde3f370e97edf4df2ee4eeb6b1400d5c5f41882552f578ba2f.

**Indexer.** The backend must serve the stake nullifiers in insertion order
with their leaf index (it already streams the nullifier events; add `index`,
keep failed txs' events, and page by index), or proxy Query/
StakeNullifierTree.

## 16. Audit round 5: wallet-facing rules (2026-10-03)

Design principle throughout: people are private; power and public money
are public. Each item changes consensus.

**Referral note: the chain makes it (P1).** Before, the registrant's wallet
made the referral note (`affiliate_pc`, `affiliate_ciphertext`) and the chain
never related its pc to the handle, so every registrant could name any live
handle and send the referral half to itself. Now:

- `MsgRegister` carries `affiliate_handle` (15) only; fields 11 and 12 are
  reserved.
- Binding affiliate field: `0` for no referrer, else
  `H(TAG_AFFILIATE, Bytes(affiliate_handle))` (TAG_AFFILIATE = "earth.affiliate").
  The handle stays in the binding, so a relayer cannot swap the referrer.
- At execution the chain resolves the handle (live) and mints the referrer's
  half to `pc = PC(handle.owner_pk, rho, rcm)` with
  `rho = H(TAG_REFERRAL, nullifier, leaf_index, 0)`,
  `rcm = H(TAG_REFERRAL, nullifier, leaf_index, 1)`
  (`zk/privacy.ReferralOpening`; TAG_REFERRAL = "earth.referral"; nullifier =
  the passport nullifier, leaf_index = the new identity leaf). Unique per
  registration (the leaf index never repeats).
- The note has no ciphertext. Its `shielded_mint` event carries `owner_pk`,
  `rho`, `rcm` (hex) beside `amount` and `position`; `ciphertext` is empty.
  The handle owner's wallet takes every mint whose owner_pk is its own,
  recomputes pc and cm and checks cm at that position (`shielded_note`).
  The `register` event adds `handle`, `referral` (amount) and
  `referral_position`.
- Why publishing the opening is safe: the recipient (the handle's address)
  and the amount are public already; spending needs nk, and the nullifier
  `H(TAG_NF, nk, rho, position)` cannot be linked without it.
- Alternatives considered: checking a wallet-supplied pc against an opening
  in the msg (more fields, same disclosure) and drawing the same total with
  or without a referrer (kills the referral incentive). The chain-derived
  opening has the fewest fields and no way to get it wrong.
- A handle that stops resolving between the ante and execution (released by
  an earlier tx in the block) lands the registration unreferred, drawing the
  unreferred rate, rather than failing after its fee.

**LP payouts above a note's maximum (D1).** The chain mints no note above
`MaxNoteValue = 2^63-1`: every wallet (Android Long, iOS Int64, web) holds
note and stake note values only up to 2^63-1 and ignores a note above it
(PRIVACY_FORMATS section 3, Amounts), so such a note would be invisible to
its owner. A chain-priced payout (a matured LP withdrawal's legs) is minted
as `ceil(v / (2^63-1))` notes to the same pc and ciphertext
(`MintNoteSplit`, at most 128: about 2^70, the capacity the earlier 64 x
(2^64-1) split had), each with its own `shielded_mint` event and position:
the wallet decrypts the one ciphertext and checks each position's cm with
that position's amount. A payout that still fails is never dropped: the
entry keeps its escrowed shares, its `payout_attempts` (LpUnbonding field
10) goes up and its completion_time moves to now + 1h << min(attempts-1, 8)
(`lp_unbond_payout_failed` carries `attempts` and `retry_at`). A withdrawal
whose note leg is already above a quarter of that, 32 x (2^63-1), is
refused when it starts (dex 1101). Every other private mint is either atomic
with its msg (swaps, deposits, refunds, unbond claims, the user's own
shield: an oversized value fails the tx and nothing is lost) or bounded far
below 2^63 (registration rewards are 1e-4 of an option; ANML claims are one
ANML).

**The 2^63-1 cap, by mint path.** `MintNote` (via `noteFor`),
`MintOpenNote`, `MsgShield.ValidateBasic` and x/shieldedstaking's stake
note mint (`mintStake`, and `fitsNote` on a delegation's derth and an
undelegation's claim) refuse a value above 2^63-1 (`types.FitsNote`); a
genesis position's derth must be 1..2^63-1, so unlocking it can always
mint its stake note.

**The 2^63-1 bound in the circuits.** Bundle outputs and stake note outputs
are private, so the chain cannot refuse a large one: the public inputs
(`cm_out`, a hash; `cv`, blinded by `rcv`) do not reveal the value. The
circuits do. privacy_core `NOTE_VALUE_BITS = 63`: `note_cm` and `stake_cm`
range-check the value to 63 bits before hashing it, so no circuit opens or
creates a commitment to a value above 2^63-1:

- action: `o_value` (the fix: it was range-checked only as a u64, so a
  bundle could create a note of up to 2^64-1 that no wallet sees) and
  `s_value`;
- stake: both inputs and both outputs (`in_amount`, `out_amount`);
- vote: the voted note's `amount`, hence `weight <= amount < 2^63`.

The input bounds are implied (every note in either tree comes from a
bounded circuit output or a bounded chain mint, above) but checked anyway:
the soundness argument needs no induction over the tree's history, and the
cost is nil. Each check is one range constraint on the value's existing
witness. Gates (bb v5.0.0, noir-recursive): action 8,120 -> 8,098 (still
2^13), stake 9,647 -> 9,672, vote 9,046 -> 9,072 (2^14); membership
(5,659) does not hash notes and is unchanged. The circuits' interface is
unchanged; the action, stake and vote verifying keys are new (genesis
sha256 77af7586...652d). Not notes, not bounded: the stake circuit's public
`v_in`/`v_out` and a bundle's public `Balance` value (u64), transparent
amounts the chain sees and checks.

Tests: nargo (privacy_core, action, stake, vote) refuse 2^63 and 2^64-1 at
every bounded value with every other constraint satisfied (fixtures commit
without the bound). `TestOrchardOutputAboveNoteMax` (zk/ultrahonk): a
balanced bundle with an output of 2^63, its binding signature valid; action
0's witness, solved by a twin circuit without the bound and proven by bb
with the real circuit, does not verify, while the honest action 1 solved
the same way does (`scripts/orchard-bundles.sh`).

**One live handle per passport (P2).** MsgBindHandle needs the claim bound
(`max_predecessor < now - handle lease - 86400`) unless the prover holds a
**live** handle: a renewal or change during the renewal period is bounded
like a claim. MsgMoveHandle refuses a handle that is not live. A caretaker
split past its expiry that the sweep has not reached is not held either
(a refresh of it is a new split, bounded).

**Lease bounds (P3).** `Query/LeaseBounds` (`/earth/personhood/v1/lease_bounds`):
`block_time`, `activation_margin_seconds`, `handle_lease_seconds` (the
longest ever in force), `handle_claim_bound`, `caretaker_lease_seconds`
(including a held longer lease after a cut), `caretaker_cast_bound`,
`caretaker_lease_hold_until`. Wallets compute max_predecessor from these
lease lengths, never from Params.

**Smaller rules.** CheckTx refuses an anchor that lapses within 120 s of the
last block (pick a newer one). MsgShield refuses a send-disabled denom. The
dex swap fee rounds up. An expedited proposal the chamber ratifies and x/gov
demotes votes again in round 1 (a new nullifier scope; `Query/BallotInputs`
reports it). Chamber votes pass the circuit breaker; the private gas prices
are capped (proof 10M, note 1M, bundle 1M). Handle binds cost nine note
writes of gas.

## 17. Audit round 6: wallet-facing rules (2026-10-03)

Same principle: people are private; power and public money are public. Each
item changes consensus unless marked. No circuit changed: the verifying keys
and the genesis (sha256 77af7586...652d) are unchanged.

**Registration binding names the chain (B6-4).** The passport proof's
`address` input is now

    H(TAG_REG, Bytes(chain_id), idc, pc_anml, Bytes(ciphertext_anml),
      pc_erth, Bytes(ciphertext_erth), affiliate)

so a MsgRegister seen on one network (a testnet) cannot be replayed onto
another within the current_date skew with a fresh fee bundle. The circuit
treats `address` as opaque, so this is a wallet-side hash change only.
Pinned vector (`zk/privacy` TestRegistrationBindingPinned): chain_id
"earth-1", idc 1, pc_anml 2, ciphertext_anml "anml", pc_erth 3,
ciphertext_erth "erth", affiliate 0 ->
`148b3513a501b6ff9c02314f355cb83fb544e22b2a9df79552fe49c944424159`
(was `20ce5fcc...5b0c`).

**An identity switch stays under its Document Signer (B6-1).** A switch (a
MsgRegister for a passport nullifier with a live registration) whose proof's
DSC differs from the live registration's is refused (personhood 1127,
`ErrSwitchSignerMismatch`). A re-proof of a passport is signed by the DSC
that signed it; only a compromised signer could prove someone else's
nullifier, and as a switch that took their registration outside every rate
cap, or moved registrations off a signer about to be purged. A switch also
counts against its signer's daily cap (shared with its registrations; the
network and country counters do not move): over it, 1113 (retry tomorrow).
Wallets: re-prove a switch with the same passport (same SOD), as they do.

**Stake vote weight: three significant digits (C-L3).** MsgStakeVote.weight
must have at most three significant decimal digits
(`RoundVoteWeight`: 399,999,999 -> 399,000,000); any other is refused at
ValidateBasic. Minted note amounts are public, so an exact weight links a
vote to the mint and to the same note's other votes; with every wallet
rounding down alike, weights fall in shared buckets. The circuit only asks
0 < weight <= amount.

**Bundles: one anchor (A-L3).** Every action of a bundle proves against the
same anchor (ValidateBasic). Both wallets already do this.

**Private gas limit (A-L1).** A private tx's gas_limit may be at most 5x the
gas it uses (refused in the ante, before the proofs, with "exceeds what this
private tx uses"). The gas is known by then: tx size, the fixed per-bundle
and per-action charge and the action's own charge; handlers run on an
infinite meter. Wallets declare the simulated gas plus 10%, well inside.

**Handles report their owner (wallet dependency).** `HandleEntry.owner`
(Query/Handle, Query/Handles, field 6): the handle-scope nullifier holding
the handle, 64 lowercase hex characters, "" for a handle never claimed.
`handle_bound` and `handle_released` carry it as `owner`; `handle_moved`
as `owner` (the new owner) and `previous_owner` (the mover). Nothing new is
disclosed: the owner is the MsgBindHandle membership nullifier (claim or
renewal) or the MsgMoveHandle new_owner, both public in the tx (and
`handle_bound` and `handle_moved` already carried them as `nullifier`). The handle scope is one fixed
scope, so the nullifier links only an identity's own handle txs to each
other, which the handle itself already does. A wallet compares it with its
own handle-scope nullifier to know a handle is its own.

**SendEnabled (A-L2).** A send-disabled denom is refused at every pool edge:
shield, unshield, a module release (a dex note swap, a private delegation's
ERTH: refused in the ante's release-map check, before anything is spent)
and a module mint into the pool. Notes shielded before the switch still move
privately inside the pool.

**Smaller rules.**
- `max_private_actions_per_block` is at most 256.
- `volume_depth_cap_per_day` is at most 1,000.
- A private LP payout that would take the sweep past its note budget (256)
  waits for the next block, unless it is the sweep's first.
- A LockPosition above 2^63-1 derth is refused (it could never unlock).
- Groundworks: a self-bond withdrawn from a validator in any status takes its
  weight with it (D6-1; the weight is the SDK's bonded sum with the removed
  delegation left out). Status changes move no tokens and need no resync.
- An allocation voter's split drops options pruned since it was cast (at its
  next resync, and in the export).
- Not consensus-visible to wallets: a recurring identity root moves its
  by-time entry (B6-2); a registration the expiry or purge sweep cannot
  retire is passed over for a day, then retried (B6-5); genesis refuses
  records dated after genesis and handle leases past genesis +
  handle_lease_max (B6-3); a stake snapshot taken after a failed root
  recording takes no roots, so no note votes on that proposal (C-L4).

**Known lag (B6-5).** Membership checks the anchor, not the registration's
expiry: an expired registration proves until the expiry sweep zeroes its
leaf (at least budget/8 per block; normally the block it expires). The leaf
is private, so the sweep is the only bound.

**Accepted, not changed.**
- X-1, transparent tx replay across earth-1 relaunches: accepted by the user
  (they were the only user on the earlier chains); account numbers are not
  offset.
- Info items left as they are: fee amounts are wallet-chosen (wallets should
  round to a fixed schedule, A-I6); timing between a move and a switch, or a
  registration and a claim, links a passport to a handle (wallets should
  randomize delays, B6-6); private stake votes keep their expedited-round
  vote after a demotion while human votes rescope (C-I4); derth is
  owner-locked but nk can be sold off-chain (C-I1); contract, ICA and group
  operators (C-I3); dust undelegations (C-I5); TWAP precision (I-D3) and the
  export-to-relaunch gap credited at the pre-export price (I-D4); a single
  YES carries an uncontested removal ballot (I-AS2, by design).

## 18. Staking without background transactions (2026-10-03)

User decision during the freeze: no background transactions and no automatic
fee spending by the wallet. Two staking flows needed one: claiming a matured
undelegation, and voting stake notes one by one in a spaced run. Both are
now one user-started tx. Same principle: people are private; power and
public money are public. Consensus-affecting; fresh genesis, no migration.

### 18.1 Undelegations pay out by themselves

**Before.** MsgUndelegate minted an owner-locked `unbond/<valoper>/<epoch>`
claim note into the stake tree; after maturity the wallet sent
MsgClaimUnbonding (a stake proof spending the claim, its fee from the claimed
ERTH) to get a spendable pool note.

**Now.** MsgUndelegate names the payout destination, as an LP withdrawal
does, and the chain pays it:

    MsgUndelegate {bundle (fee), validator, amount, stake, pc (6), ciphertext (7)}
    sighash fields: StakeFields(stake), Bytes(validator), amount, pc, Bytes(ciphertext)
    MsgUndelegateResponse {value (2), payout_id (4)}; denom (1), position (3) reserved

- The stake proof is unchanged (circuits/stake, same verifying key): it
  spends derth, v_out = amount, change back to the owner. `spc_mint` is
  proven (the circuit always binds it to the owner) but unused; the wallet
  passes a fresh pc of its own, as for MsgRestake. `spc_ciphertext` must be
  empty. `pc` is a pool pc (any owner; normally the wallet's own) and
  `ciphertext` the v2 amount-blind note ciphertext (177 bytes), both bound by
  the sighash and checked like any mint (`CheckMint`).
- The chain books the derth's live value u into the epoch's UnbondRecord
  (requested, target, outstanding += u; pending_undelegation; derth supply
  -= amount) exactly as before, and queues an `UnbondPayout {id, validator,
  epoch, value = u, pc, ciphertext, payout_attempts, retry_at}`.
- Unbonding is unchanged: the epoch end undelegates each record's target
  from x/staking; maturity reads the SDK entry's balance into record.payout.
- **Payout.** In the EndBlocker after the record matures (x/staking pays the
  entry after this module's EndBlocker, so never in the maturity block), the
  chain mints `u x payout / requested` uerth to pc with ciphertext
  (MintNoteSplit: notes of at most 2^63-1, each its own `shielded_mint`
  event and position), settles the record (outstanding -= u, paid += pay)
  and, on the record's last payout, sends the floor division's dust to the
  community pool and removes the record. Event
  `shieldedstaking_unbond_payout {payout_id, validator, epoch, value,
  amount, notes, positions}` after the mint events.
- **Bounded, never dropped.** At most 50 payouts tried and 256 notes minted
  a block (a payout whose notes would pass the budget waits, unless it is the
  block's first). Due retries go first, then untried payouts of matured
  records, oldest record first. A payout that fails (pool full, uerth sends
  disabled by governance, ...) is kept: `payout_attempts`+1, `retry_at` =
  now + 1h << min(attempts-1, 8) (capped at 256h), moved to the retry queue
  (it cannot hold up the payouts behind it), event
  `shieldedstaking_unbond_payout_failed {payout_id, validator, epoch,
  attempts, retry_at, error}`.
- **Start refusal.** An undelegation worth more than 2^63-1 is refused at
  start (as before: one note's worth). A slash only lowers the payout.
- **Slashing, unchanged economics.** A claim paid `amount x payout /
  requested`; a payout pays `value x payout / requested`. A slash while the
  record is PENDING cuts its target (BeforeValidatorSlashed), a slash of the
  SDK entry cuts the entry's balance, so record.payout and every payout fall
  pro rata, as every claim did. Dust to the community pool, as before.
- **Removed.** MsgClaimUnbonding and its response, the `unbond/` stake denom
  (and its asset exclusion), claim notes in the stake tree,
  `ExecutesInAnte` for staking, `gasClaim`. x/shielded's generic
  FeeFromOutputMsg path stays (no msg implements it now). ErrNotMatured
  (1104) now reads "unbonding record is not open".
- **State.** `UnbondPayouts` (id), `PayoutsByRecord` (validator, epoch, id:
  untried), `PayoutRetries` (retry_at, id), `MaturedRecords` (records with
  untried payouts), `UnbondPayoutSeq`. Genesis: `unbond_payouts` (17),
  `next_unbond_payout_id` (18); Validate requires each record's payouts to
  sum to its outstanding, values 1..2^63-1, a pc, a blind ciphertext, and
  retry_at set exactly when attempts > 0. Invariant 8 checks the same plus
  one queue per payout. `Query/UnbondPayout {id}` returns the payout and its
  record (`/earth/shieldedstaking/v1/unbond_payouts/{id}`); not found once
  paid.
- **Gas.** Unchanged for MsgUndelegate (its "mint" write slot now prices the
  queued payout); the payout itself is free to the owner.

**Privacy: equal or better.** The undelegated amount and its value were
public at undelegate before and are now; the payout amount follows from
public numbers (value, record payout/requested) either way, as the claim's
v_out did. The pc is hiding (PC(owner_pk, rho, rcm)); the payout notes'
later spends are unlinkable without nk. The msg's pc and ciphertext link the
undelegate tx to its payout notes, which the claim's amount already did.
Gone: the claim tx itself (its own timing, fee and, with a fee bundle, its
anchor), and the claim note in the stake tree.

**Wallet.** At undelegate, choose a fresh pool note opening (rho, rcm) of
your own, pc = PC(owner_pk, rho, rcm), ciphertext = EncryptBlindNote(rho,
rcm, memo) to your own viewing key; remember payout_id. Find the payout by
trial-decrypting `shielded_mint` ciphertexts as for any chain-minted note
(the amount is on the event; several notes share one ciphertext when it was
split), or by `shieldedstaking_unbond_payout.payout_id`. Nothing to send.

### 18.2 One stake vote per person

**Before (§15, §17).** One MsgStakeVote per note, each with its own proof,
public weight (3 significant digits) and vote nullifier; a wallet with many
notes ran a spaced background run of votes.

**Now.** One msg votes up to four notes of one owner at one validator with
ONE weight, the notes' sum rounded down to three significant digits.

Circuit `vote` (mobile `circuits/vote`, MAX_NOTES = 4, 27,543 gates: 2^15,
within the bundled 2^15 + 1 point SRS; 5 slots would need 2^16):

    private  nk, per slot i: amount_i, rho_i, rcm_i, pos_i, path_i[32],
             low_value_i, low_next_value_i, low_next_index_i, low_index_i, low_path_i[32]
    public   note_root, nf_root, asset, weight (u64), proposal_id (u64), vnf[0..3], sighash

    slot used (amount_i != 0): the note under note_root, its spend
      nullifier absent under nf_root, vnf_i = H(TAG_VNF, nk, rho_i, pos_i, proposal_id)
    slot unused (amount_i = 0): vnf_i = 0
    0 < weight <= sum amount_i                                   (u128)
    bind(sighash)

One nk for every slot: one owner. Same tags and hashes as §15 (Go parity
vectors unchanged). 37 nargo tests: the §15 ones per slot, plus three and
four notes, a rounded weight, unused slots anywhere, the same note twice
(same vnf), a slot of another owner, an inflated slot, a wrong slot vnf, an
unused slot carrying a vnf, weight above the sum, no notes, two maximal
notes (sum 2^64 - 2).

    MsgStakeVote {bundle (fee), proposal_id, validator, options, weight, proof (8),
                  vote_nullifiers (10): exactly 4}         vote_nullifier (9) reserved
    public inputs: snapshot.root, snapshot.nf_root, AssetID(derth/<validator>),
                   weight, proposal_id, vote_nullifiers[0..3], sighash
    sighash fields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)),
                    weight, vote_nullifiers[0..3]

- ValidateBasic: exactly 4 slots, canonical field elements, used ones first
  (at least one), distinct, zeros after; weight > 0 with at most three
  significant digits (RoundVoteWeight); options as before.
- The chain refuses a vote nullifier already used on the proposal
  (`UsedVoteNullifiers`, every note's; ErrVoteNullifierUsed 1119, the whole
  msg) and weight above the validator's snapshot supply. It records ONE
  StakeVote under 0x00 || vote_nullifiers[0] with `vote_nullifiers` (7) and
  derth = weight. Tally and validator inheritance are unchanged: the vote is
  one weight at one validator, deducted from it once.
- Event `shieldedstaking_stake_vote`: `vote_nullifiers` (comma-separated
  hex, in slot order) replaces `vote_nullifier`.
- Gas: gasVote + proof_verification_gas + (1 + used slots) x note_gas.
- Genesis: a note vote has 1..4 vote_nullifiers, the first its key's, none
  shared with another vote on the proposal; a position vote has none.
  InitGenesis rebuilds `UsedVoteNullifiers`; the snapshot sweep clears them
  with the votes.

**Why per validator.** The tally deducts private votes from each validator's
inherited vote, so it needs each validator's voted derth: a vote is one
weight at one validator. A wallet staked with several validators sends one
vote per validator (each its own weight). Putting several validators in one
msg would publish the same per-validator weights and link them in one tx.

**More than four notes.** Trade-off: the wallet merges first (MsgRestake,
two notes into one, a fee each, user-started; also any undelegation with
change consolidates) so one vote covers everything, or it votes the rest in a
second MsgStakeVote, which publishes a second weight for the same validator
and proposal, linkable by timing. The chain allows both (it cannot tell two
votes of one owner apart). Wallets should keep at most four notes per
validator (merge on the user's next staking action) and fall back to a
second vote. Five slots would double the circuit (2^16) past the bundled
SRS.

**Positions not unified.** A position is a public object (validator, derth,
owner tag), voted by MsgPositionVote with a stake proof of its owner tag,
replaceable, already one tx for every open proposal. Folding positions into
the note vote would tie the hidden notes' vote to a public position (its id
and exact derth) in one tx, worse than voting them apart, and needs a second
proof system in the circuit. Unchanged.

**Privacy: better.** One weight per owner and validator instead of one per
note, so a wallet's note count and individual note amounts no longer show;
the weight is the rounded sum. Votes stay unlinkable to the notes' spends
and to the same notes' votes on other proposals (nk needed). The number of
notes voted (1..4, the count of non-zero vote nullifiers) is visible: an
unused slot must carry vnf 0, so it cannot be padded. That is at most 2 bits,
against every note's exact (rounded) amount before.

**Wallet format (building a vote).** As §15, per note, for up to four of the
owner's derth/<validator> notes under the snapshot (position < tree_size,
nullifier absent under nf_root, vnf not yet used on this proposal): fill
slots 0..k-1 with them and slots k..3 with amount 0, vnf 0 (other witness
fields 0); weight = RoundVoteWeight(sum of amounts); vote_nullifiers =
[vnf_0..vnf_{k-1}, 0...]. Prover.toml arrays: `amount`, `rho`, `rcm`, `pos`,
`path` (4 x 32), `low_value`, `low_next_value`, `low_next_index`,
`low_index`, `low_path` (4 x 32), `vnf` (4). Android PrivacyProver: VOTE has
10 public inputs (was 7); the 2^15 circuit fits SRS_SIZE = 2^15 (the hint
must be at least the circuit's dyadic size; bb proves a 27.5k-gate circuit
with 32,768 points). Proof length unchanged (14,656 bytes).

**Genesis.** New vote verifying key; genesis.json sha256
1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8. Action,
stake and membership keys unchanged.

## 19. Private redelegation (2026-10-03)

(Superseded in part by section 20, 2026-10-04: the bonded part moves with
x/staking's primitives and a module-recorded entry, with no transitive lock
and no max_entries; a slash of the source is owed by the move's notes (the
slash debt), not absorbed by the destination's book; the credit is merged
into the owner's note, labelled, never minted.)

The one exception to the feature freeze (user decision): a private staker
moves derth from validator A to validator B with no unbonding gap and keeps
earning. Same principle: people are private; power and public money are
public. Consensus-affecting; fresh genesis, no migration. No circuit
changed: verifying keys and genesis (sha256
1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8) are
unchanged.

### 19.1 The msg

    MsgRedelegate {bundle (fee), src_validator (2), dst_validator (3), amount (4), stake (5)}
    sighash fields: StakeFields(stake), Bytes(src_validator), Bytes(dst_validator), amount
    MsgRedelegateResponse {value (1), derth (2), position (3), completion_time (4)}
    type URL /earth.shieldedstaking.v1.MsgRedelegate

- The stake proof is circuits/stake as for MsgUndelegate: asset =
  AssetID(derth/<src>), v_in = 0, v_out = amount; it spends one or two
  derth/<src> notes, change back to the owner as derth/<src> notes (hidden
  amounts). `spc_mint` is the owner's fresh stake pc for the derth/<dst>
  note and `spc_ciphertext` its blind stake ciphertext (177 bytes,
  required), exactly as for MsgDelegate. The circuit already binds
  spc_mint to the spender's owner_pk, so the derth/<dst> note can only be
  the spender's: stake stays owner-locked across the move.
- ValidateBasic: both validators canonical and different (1120 otherwise),
  amount > 0, the bundle pays only the fee (its uerth balance), the proof
  spends at least one note, spc_ciphertext present.
- The chain refuses (code; nothing spent or paid, see 19.3): a destination
  it will not delegate to (1102: unknown, jailed, tombstoned, slashed to
  nothing, or a book settling: no derth but backing); more derth than
  exists (1103); a value or a derth/<dst> mint below min_delegation (1 ERTH;
  1103) or a mint above 2^63-1 (1103); and x/staking's limits (1120,
  below). Gas: 700,000 + proof_verification_gas + 7 x note_gas.
- Response: `value` (uerth moved), `derth` (derth/<dst> minted),
  `position` (its stake-tree position), `completion_time` (unix ns the
  x/staking entry completes; 0 when none was made).

### 19.2 What the chain does

1. The module's unwithdrawn rewards at A and at B are withdrawn into their
   delegation queues (W -> P: neither backing changes), so no x/staking call
   below pays anything outside the books.
2. u = floor(amount x B_A / S_A), A's live rate (as an undelegation).
3. Out of A's queue first: min(u, P_A) is ERTH waiting to be delegated to A
   at the epoch end. It moves to B's queue as a book entry: never bonded at
   A, it needs no x/staking entry and carries no slash risk from A. A value
   exceeding the queue by at most 0.001 ERTH moves out of the queue alone,
   the excess left to A's book.
4. The rest leaves the module's bonded stake at A with x/staking's
   BeginRedelegate (delegator = the module, A -> B), in the same block: no
   unbonding. It never exceeds D_A - U_A (pending undelegations stay
   covered).
5. derth/<dst> = floor(arrived x S_B / B_B), arrived being the measured rise
   of B's backing (u less at most x/staking's truncation), B_B and S_B as
   before the move: B's live rate. S_A -= amount, S_B += minted; the supply
   checkpoints for open snapshots are written first.
6. The proof's nullifiers are spent, its change appended; the derth/<dst>
   note is minted to spc_mint (stake note event with spc and ciphertext).

Rounding favours the books on both sides (floors). A's rate and B's rate do
not move, beyond x/staking's one-uerth truncations. The stake earns at B
from that block on (a queued part from B's epoch end, as any delegation).

Event `shieldedstaking_redelegate {src_validator, dst_validator, derth
(amount), value (u), minted, queued, bonded, completion_time (unix ns, ""
when no entry)}`, then the stake nullifier, stake note (change) and stake
note (mint) events.

### 19.3 x/staking's rules, shared

The module is one delegator to x/staking, so x/staking's redelegation rules
apply to it as a whole, shared by every private staker:

- **No transitive redelegation.** While any private redelegation INTO A is
  maturing (unbonding_time, 21 days), x/staking refuses a redelegation out
  of the module's bonded stake at A. One person's redelegation into A holds
  every private staker of A for up to 21 days (undelegating is not
  affected; a value that fits in A's queue still moves). Refused with 1120
  "transitive".
- **max_entries per pair.** At most max_entries (32) maturing entries per
  (A, B); the next is refused with 1120 "max_entries" naming when the
  earliest completes. Each bonded move is one entry (x/staking does not
  merge redelegation entries).

The redelegation runs in the private ante, atomically with the spend
(ExecutesInAnte, as the dex's swaps): a refusal in a block (the limits are
shared, so they can change between CheckTx and the block) fails the tx in
the ante, spending nothing and paying no fee. CheckPrivateAction refuses
the same earlier, before any proof is verified.

`Query/Redelegation {src_validator, dst_validator}`
(`/earth/shieldedstaking/v1/redelegation/{src}/{dst}`): `src_locked_until`
(unix ns when the last maturing redelegation into src completes, 0 if
none), `entries` and `max_entries` for the pair, `pair_frees_at` (earliest
completion, 0 if none), `queue` (src's queue plus unwithdrawn rewards:
value that moves without x/staking). A wallet checks it before proving: if
src is locked and the value exceeds `queue`, or the pair is full, the move
is refused.

Accepted, documented: griefing. Redelegating dust into A (min_delegation)
every 21 days locks A's private stakers out of redelegation (not out of
undelegation); filling a pair's 32 entries blocks that pair. The fix is to
spread the module's stake over several delegator accounts ("lanes", each
validator's stake split between accounts so that one with no incoming entry
can always move); it touches every place the module reads its delegation
(backing, the epoch end, unbonding records, the tally, slashing, genesis)
and is left for after the relaunch.

Validator self-bonds still cannot redelegate (round 6, D6-1): MsgBeginRedelegate
is refused by the ante filter and by the staking hook for every account but
this module, and genesis refuses any redelegation that is not the module's.

### 19.4 Slashing: B's book absorbs it, pro rata

x/staking slashes a redelegation entry for an infraction of A committed
before it (creation height at or after the infraction height) while it
matures: slash_fraction (5% for a double sign) of the entry's shares at B.
Rule: **B's book absorbs it, pro rata.** The shares are unbonded from the
module's delegation at B and burnt, so every derth/B note loses the same
fraction through B's rate; the end of the block lowers B's epoch rate
(positions re-weigh). The redelegated notes are derth/B like any other:
nothing links them to the redelegation (that is the privacy), so the loss
cannot follow them; their owner bears their pro-rata share, as everyone at
B does.

x/staking's own order would be unfair for a pooled delegator: it takes the
entry's slash first from the delegator's unbonding entries at B begun after
the infraction, up to the whole slash amount, and then still takes the full
slash_fraction x shares from the delegation unless those entries covered
all of it. For one person those are their own undelegations; for the
module they are other people's undelegations from B, people who never
staked at A. So as a slash begins (x/staking's BeforeValidatorModified, in
x/slashing's or x/evidence's BeginBlocker), for every destination of the
module's redelegations from the slashed validator, the module:

- withdraws its rewards at the destination into the queue (the slash's
  unbond would pay them outside the books);
- sets its unbonding delegation at the destination aside
  (`ShelteredUnbondings`) and puts it back as it was at the start of the
  next slash, in its own BeginBlocker (now ordered right after x/slashing
  and x/evidence, before any tx) and at the start of its EndBlocker;
- marks the destination for the end-of-block re-weigh.

B's undelegations already under way are therefore untouched (they left B;
the payouts are as without the slash). The value moved out of A's queue
carries no entry and no slash. Invariant 9 checks nothing stays set aside.

Rejected alternative: A's book absorbs it (hide the entry instead, so the
slash burns from A's tokens). Then the redelegator bears nothing, A's
remaining holders pay for the leavers (a run on A after any infraction), and
x/staking caps the burn at A's remaining tokens, so after an exodus the
slash simply vanishes. B-absorbs keeps x/staking's amount exact, keeps the
redelegator's share with them, and bounds the moral hazard by the evidence
window (double-sign evidence is normally committed within blocks; at most
max_age: 48 h and 100,000 blocks).

### 19.5 Votes, Groundworks, books

- **Votes.** A derth/A note in a proposal's snapshot votes as A, once
  (its vote nullifier), whether before or after it moves: its spend
  nullifier entered the tree after the snapshot's nf_root. Its derth/B note
  was minted after the snapshot (not under its note root) and cannot vote
  on that proposal. No unit of stake votes twice. The tally, as for an
  undelegation (audit F6): A's private votes are fractions of A's snapshot
  supply applied to the module's CURRENT shares at A, so a vote of stake
  that moved away counts against what is left at A (the leaver keeps a
  diluted voice, taken from A's remaining stake), and the moved stake at B
  follows B's own (inherited) vote. Total power never exceeds bonded stake.
  Proposals snapshotted after the move see derth/B normally.
- **Groundworks.** Weight lives in positions, per validator; a note
  redelegation changes no position, no total and no epoch rate. A
  position's weight moves by unlocking it (a derth/A note), redelegating
  the note and locking it at B with its split: A's voter loses the weight,
  B's carries it at B's epoch rate. (Positions are public objects; a direct
  position move was left out to keep the msg to notes.)
- **Books.** Both books keep `derth_supply x rate = backing` (invariant 4);
  the module's balance stays the queues plus matured payouts (invariant 1);
  undelegation records and payouts are untouched (invariants 3, 8).
  Invariant 9 (new): every x/staking redelegation is the module's, between
  two different validators, with 1..max_entries entries, and no unbonding
  delegation is left set aside.
- **Genesis.** x/staking exports the module's redelegations in flight;
  InitGenesis accepts them (and refuses any other: "genesis redelegation
  ... only private staking redelegates"). This module exports nothing new.

### 19.6 Privacy

The amount and its value are public, as at undelegate: the books and
x/staking's delegations are public (power and public money), and the
minted derth/<dst> amount follows from them. Who moves stays hidden: the
spent notes show only nullifiers, the change is a hiding commitment, the
derth/<dst> note goes to a hiding stake pc with an amount-blind ciphertext;
the event names validators and amounts, never an owner. As at undelegate,
one tx links the spent notes to the new note; the new note's later spends
are unlinkable without nk.

Why no new circuit. A dedicated circuit that minted the derth/<dst> note
itself would have to prove the rate conversion against B_A, S_A, B_B, S_B,
which change every block (rewards accrue): the proof would be stale before
it landed, or the chain would have to re-check a slippage bound, which is
the chain computing the amount anyway. Hiding the amount is impossible
while the books and x/staking's delegations are public. The existing stake
circuit already proves the one statement needed (these derth/<src> notes are
mine, amount leaves, the change and the mint pc are mine): nothing to gain.

### 19.7 Wallet format (building a redelegation)

1. `Query/Validator` for src (rate) and dst; `Query/Redelegation{src, dst}`:
   if `src_locked_until > now` or `entries == max_entries`, and the value
   (amount x rate_src) exceeds `queue` by more than 0.001 ERTH, the chain
   will refuse: offer undelegating, another destination, or waiting.
2. Choose up to two derth/<src> notes (merge first if more), amount <= their
   sum, value >= 1 ERTH and minting >= 1 derth/<dst> at dst's rate.
3. Stake proof: asset AssetID(derth/<src>), inputs those notes, output 0 the
   change (derth/<src>, wallet stake ciphertext), v_out = amount, mint_rho/
   mint_rcm a fresh opening for the derth/<dst> note (spc_mint =
   StakePC(owner_pk, mint_rho, mint_rcm)), spc_ciphertext =
   EncryptBlindStakeNote(mint_rho, mint_rcm, memo) to your own key.
4. Fee bundle (uerth only, the whole balance is the fee); sighash fields as
   in 19.1. Prove, send.
5. Find the derth/<dst> note: the `shieldedstaking_stake_note` event whose
   denom is derth/<dst> with your spc (or by trial decryption of its blind
   ciphertext; the amount is on the event and in the response's `derth`).

## 20. One stake note per validator; note-enforced slash debt (2026-10-04)

User decisions: one stake note per validator (staking more with a
validator merges into the existing note in the same tx), and redelegation
slashes paid by the notes the redelegation credited ("note-enforced slash
debt"), folded into the same circuit change. Same principle: people are
private; power and public money are public. Consensus-affecting; fresh
genesis, no migration. Supersedes §19.2 step 3, §19.3 and §19.4 (x/staking's
transitive and max_entries limits, and "B's book absorbs it").

### 20.1 The design, and why

**The chain mints no stake note.** Every stake note is an output of the
stake proof. Value the chain credits (a delegation's derth, an unlocked
position's, a redelegation's arrival) is a public `v_in` the proof merges
into the owner's existing note: one note per (owner, validator), with no
merge msgs, no split votes and no 2-input limits in practice.

The credited derth follows the live rate, which moves every block
(rewards accrue in W_v), so the chain computes it. Three ways to merge a
chain-computed credit into a hidden note were weighed:

- **Homomorphic credit** (the chain adds the minted derth to the hidden
  amount). Stake commitments are Poseidon2 hashes, not additive. A Pedersen
  amount inside every stake commitment would need EC arithmetic in the stake
  and vote circuits, a new note format for every wallet, and a public bound
  on the hidden old amount (so the chain-added sum stays within 2^63-1).
  Rejected: much more circuit and wallet surface for no privacy gain (the
  credit is public either way).
- **The epoch-fixed rate** (known at proof time). Minting at a stale rate
  lets a delegation made just before the epoch end buy a whole epoch's
  accrued rewards it did not earn (rate.go). Rejected: unsound.
- **The wallet names the credit; the chain checks the price** (chosen).
  MsgDelegate.derth (MsgRedelegate.dst_derth) is the credited derth, a
  public input of the proof; the chain refuses it unless the value buys it
  at the live rate: derth <= floor(amount x S / B) (= amount while S = 0),
  derth >= min_delegation. What the value buys beyond it stays in the book
  (every holder's rate, the delegator's included), as the floor's dust
  always did. The merged note's amount is known when proving. The wallet
  quotes with a small margin for the rate's drift until its block (20.8);
  a quote the rate outran is refused in the ante, before anything is spent,
  at no cost.

**What it reveals.** The credit amount and its value are public, as
before. A top-up spends the owner's existing note: its nullifier shows that
some derth/<v> note was consumed, never which (nf = H(nk, rho, position)
cannot be related to a commitment or position without nk; the anchor is a
whole-tree root). New: **padding**. An input of amount 0 may publish its
own would-be nullifier (still derived from nk, so nobody can publish
another owner's), and the chain requires every note-moving msg to spend in
its first slot: a first delegation looks exactly like a top-up. An output
of amount 0 may publish a zero note's commitment, and the chain requires
every note-moving msg to create one: a full exit looks like a partial one.
And since the chain mints no stake note, **no stake note's amount is ever
public** (before, a delegation's minted note carried its amount on the
event).

**Redelegation slashes, enforced by the notes.** The module stops calling
x/staking's BeginRedelegate: it unbonds at src, delegates at dst and
records the redelegation entry itself (20.5), so a pooled delegator is not
locked by one person's inbound move (no transitive rule) nor by the pair's
max_entries. The entry still lets x/staking slash the moved stake when src
is punished for an infraction before the move. The module covers that burn
so dst's honest holders lose nothing, and the move's notes owe it: the
credit carries a slash label inside its note commitment, and the slash
debt tree says what each slashed move's exposure is still worth (20.6).

### 20.2 Circuit `stake` v2 (mobile circuits/stake)

    spc    = H(TAG_SPC, owner_pk, rho, rcm)
    cm     = H(TAG_STAKE, asset, amount, spc, label)            (was H4, no label)
    label  = 0, or H(TAG_SLABEL, move_key, move_time, exposed)   TAG_SLABEL "earth.slabel"

Two lanes, one owner (nk), all inputs under `anchor`:

- **Lane A** (`asset`): up to two inputs, one output; `v_in` credited,
  `v_out` leaving. At most one labelled input, which either **keeps** its
  label (the output carries the same label and exposure; only unexposed
  value moves: in_0 + in_1 - exposed + v_in == out - exposed + v_out) or
  **clears** it once its window has closed (move_time < clear_before): the
  exposure is worth `retained` from the debt tree under `debt_root` (20.6),
  and the output is unlabelled (in_0 + in_1 - exposed + retained + v_in ==
  out + v_out).
- **Lane B, the credit lane** (`cr_asset`): one unlabelled input (or
  padding), one output = cr_in + cr_v_in. With cr_move_time != 0 (a
  redelegation) the output is labelled (move_key = cr_nf, the lane's own
  nullifier; move_time; exposed = cr_v_in).
- Padding: amount-0 inputs publish 0 or their would-be nullifier; amount-0
  outputs publish 0 or the zero note's commitment. Every note amount
  <= 2^63-1.

    Public: anchor, asset, nf_0, nf_1, cm_out, v_in, v_out, clear_before, debt_root,
            cr_asset, cr_nf, cr_cm, cr_v_in, cr_move_time, otag, sighash   (16)

16,242 gates (2^14, was 9,672 for 2 in / 2 out), 52 nargo tests.
`spc_mint` and the second lane-A output are gone. privacy_core:
`stake_cm(asset, amount, spc, label)`, `stake_label`, `debt_leaf`,
`debt_retained`; Go parity vectors in zk/debt `TestNoirParity`
(DebtLeaf(1,2,3,4) = 0x0b28cc85...e82a, StakeLabel(0x4d4b,1000,200) =
0x2dfbc154...a881, StakeCM(1,2,3,4) = 0x0ffc538b...4232, debt EmptyRoot =
0x0cea3d3e...6903).

### 20.3 Msgs, sighash, shapes

    StakeProof {proof 1, anchor 2, nullifiers 3 (exactly 2), owner_tag 7,
                commitment 9, ciphertext 10, credit_nullifier 11, credit_commitment 12,
                credit_ciphertext 13, clear_before 14, debt_root 15}
                reserved 4, 5 (commitments, ciphertexts), 6 (spc_mint), 8 (spc_ciphertext)
    StakeFields = anchor, nf_0, nf_1, cm, Bytes(ct), credit_nf, credit_cm, Bytes(credit_ct),
                  owner_tag, clear_before, debt_root

A ciphertext is present exactly for a non-zero commitment and is the
wallet stake ciphertext, **201 bytes** (was 153: the label fields are in it,
zero when unlabelled, so the length says nothing). `debt_root` is zero
exactly when `clear_before` is 0 (the proof clears nothing); otherwise it
must be the current debt root and `clear_before` <= block time - window.

| Msg | lane A | v_in / v_out | lane B | shape |
| --- | --- | --- | --- | --- |
| Delegate {.., amount 5, **derth 6**} | derth/<v> | v_in = derth | - | spend + create |
| Restake | derth/<v> | - | - | spend + create |
| Undelegate | derth/<v> | v_out = amount | - | spend + create (change or zero note) |
| LockPosition | derth/<v> | v_out = amount | - | spend + create |
| UnlockPosition | derth/<position's v> | v_in = position's derth | - | spend + create |
| Update/PositionVote | 0 | - | - | nothing |
| Redelegate {.., **dst_derth 6, move_time 7**} | derth/<src> | v_out = amount | derth/<dst>, cr_v_in = dst_derth, cr_move_time = move_time | spend + create, both lanes |

"spend" = nf_0 non-zero (the owner's note, or padding); nf_1 optional (a
second note merged); "create" = cm non-zero. Sighashes: Delegate adds
`derth` after `amount`; Redelegate adds `dst_derth, move_time` after
`amount`; the rest as before (with the new StakeFields).
MsgDelegateResponse.position, MsgUnlockPositionResponse.position and
MsgRedelegateResponse.position name the merged note. Gas: two writes per
nullifier slot and one per output slot (lane B adds one and one), plus one
for an undelegation's payout.

**MsgRestake** stays: an owner holding more than one note at a validator
(two devices, a labelled note beside an unlabelled credit, 20.6) merges
them without moving value. Splitting is gone.

### 20.4 Votes and the snapshot

Circuit `vote` v2: **2 slots** (MAX_NOTES 2; one note per validator needs
one, the second covers a note made beside a labelled one), each optionally
labelled; a labelled note votes amount - exposed + retained (the debt tree
under `debt_root`, the CURRENT one: a slash after the snapshot counts).

    Public: note_root, nf_root, debt_root, asset, weight, proposal_id, vnf[2], sighash   (9)
    MsgStakeVote: vote_nullifiers (10) exactly 2, debt_root (11)
    sighash fields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)), weight,
                    vote_nullifiers[0..1], debt_root

21,716 gates (2^15, was 27,543), 45 nargo tests.

**The snapshot rule needs no change.** A note under the snapshot root whose
nullifier is absent under the snapshot's nf root votes; its spend after the
snapshot (a merge, a top-up, a redelegation) does not stop it, because its
nullifier entered the tree after nf_root. So a top-up after the snapshot
keeps the pre-existing value's vote: the old note votes it (the wallet keeps
spent notes' openings until no open proposal predates the spend), and the
merged note, created after the snapshot, is not under the note root and
cannot vote on that proposal. No unit votes twice: the old note's vote
nullifier is spent once per proposal, and the new value never existed at
the snapshot. Letting the merged note carry the old value's vote instead
would need a proof of lineage in the vote circuit and would link the two
notes; nothing would be gained.

### 20.5 Redelegation internals

Steps 1, 2, 4 of §19.2 are unchanged (rewards into both queues, the value
at src's live rate, out of src's queue first). Step 3, the bonded part:
`moveBonded` = x/staking's BeginRedelegation without its refusals: Unbond
at src, Delegate at dst (token source = src's status), and the entry
recorded by the module:

- one entry per (src, dst, block height): moves in one block share it
  (balance and shares added);
- past `MaxEntryHeightsPerPair` (4,096) entries a move joins the latest
  entry, which keeps its height and completion (every move in an entry is
  at or after its height, and the entry matures before any of its moves'
  labels clear; only an infraction between that height and the move's falls
  on src's stake instead). Reaching the cap takes 4,096 blocks of bonded
  moves of at least 1 ERTH each, each move's exposure locked in place for the
  unbonding time;
- src unbonded: no entry (nothing can be slashed); src unbonding: the
  validator's unbonding time and height.

A move with an entry is recorded (`Move`: key, src, dst, height,
move_time, credited, shares, entry_height, completion, retained) until the
entry matures. Step 5: dst credits `dst_derth` (creditDst: <= what arrived
buys at dst's rate, >= min_delegation), supply += dst_derth; the proof's
lanes spend the src notes, the dst note (or padding) and create the change
and the merged, labelled dst note. `move_time` must be within
[block time - 600 s, block time] (MoveTimeSlackSeconds). Both books are
re-weighed at the end of the block (audit 7). There is **no lock-out**: no
transitive rule, no max_entries. Query/Redelegation and error 1120's
"transitive"/"max_entries" refusals are gone.

### 20.6 The slash debt

**Labels.** The credit lane labels the redelegated derth with its move
(move key = the credit nullifier: unique, public in the tx; move_time; the
exposure). A note holds at most one label; the exposure never leaves its
note while the label is open: lane A spends at most one labelled note and
keeps the exposure in the output; lane B merges only into an unlabelled
note (a redelegation into a validator where the owner's note is labelled
makes a second note there). Undelegating, locking or redelegating exposed
derth waits for the label to clear; the note's unexposed part moves freely.

**The window.** A label may clear once move_time + window < the proof's
clear_before <= block time, window = the longest x/staking unbonding_time
ever seen (MaxUnbonding; a governance cut does not shorten old windows) +
600 s. By then the move's entry has matured (completion = block time of the
move + unbonding_time <= move_time + window) and x/staking no longer
slashes it.

**The debt tree** (zk/debt; `debt_tree.go`): an indexed (sorted) depth-32
Poseidon2 tree with one row per SLASHED move: leaf = H(TAG_DEBTL, key,
next_key, next_index, retained), TAG_DEBTL "earth.debtl", sentinel leaf 0.
A move with a row is worth its row's `retained`; a move absent (low leaf
below it, successor above or none) is worth its whole exposure. Rows are
written only when a slash reaches a move, in BeginBlock, so the current
root is stable through a block's txs; proofs read the current root, never
an older one (a stale root could skip a row). Rows are never removed.

**Attribution.** x/staking calls BeforeValidatorModified(src) as a slash
begins (outside txs): the module opens a watch (the module's shares at each
destination of its src redelegations), then counts x/staking's Unbond of
the module's delegation at each dst (BeforeDelegationSharesModified: one per
slashed entry). It settles the watch at the next slash or at its
BeginBlocker (right after x/slashing and x/evidence, before any tx): per dst,
the slashed entries are the last `calls` unmatured entries by height
(x/staking slashes an entry iff created at or after the infraction and not
mature), the burnt shares are the fall of the module's shares at dst, and

    value = TokensFromShares(burnt) at dst
    debt  = floor(value x S / (B + value))          (B, S after the burn)

comes off dst's derth_supply (ValidatorState.slash_debt += debt): dst's
rate (B + value) / S before is B / (S - debt) after, every honest holder's
value unchanged. Each move in the slashed entries owes debt pro rata to its
shares: retained -= its part, and its debt row is written. Events
`shieldedstaking_slash_debt {src_validator, dst_validator, value, debt,
entries}`, `shieldedstaking_move_slashed {move_key, src_validator,
dst_validator, debt, retained}`, `shieldedstaking_debt_row {move_key,
retained, index, root}`.

**Solvency.** derth_supply is the book's claims: unlabelled derth at face
value, labelled exposures at what they are still worth. The exposed notes
are all still at dst (the exposure cannot leave), so every unit of debt is
owed by a note in dst's book; a note pays when it clears (its amount
becomes amount - exposed + retained) and can only clear after its entry
matured, so no slash reaches a cleared note. Invariant 4 (supply x rate =
backing) holds throughout; the "receivable" is the outstanding cut on
uncleared labels, repaid by the notes as they clear (invisible by design:
which note clears, and its amount, stay hidden).

**Chained moves (X -> A -> B within the window).** The X-exposure of a
note at A cannot move on until its window closes (exactly x/staking's own
per-delegator transitive rule, applied to the redelegated derth itself
rather than to the whole pooled delegation); the note's unexposed part, and
every other staker at A, move freely, and A -> B labels its own credit with
its own move. Letting labels travel instead would leave the debt in A's
book (where x/staking burns) while the paying note sits in B's book: the
cross-book settlement would publish either the label at the second move
(linking the two moves of one owner) or each use's haircut (revealing the
note's amount). In-place exposure needs neither.

**Votes** count a labelled note at amount - exposed + retained (20.4).

### 20.7 Genesis, queries, invariants

Genesis: `moves` (19), `debt_rows` (20, insertion order, latest retained:
InitGenesis rebuilds the same tree), `max_unbonding_seconds` (21),
ValidatorState.slash_debt (9). Validate: canonical distinct nonzero keys,
retained in 0..credited, a cut move has its row. InitGenesis loads the
moves before checking x/staking's redelegations: every unmatured module
entry's shares must equal its moves' shares exactly; at most
MaxEntryHeightsPerPair entries per pair.

Queries: `Query/DebtTree {start, limit}` -> rows (insertion order), size,
root, window_seconds, clear_before (`/earth/shieldedstaking/v1/debt_tree`);
`Query/Move {key}` -> the move while open, slashed, retained
(`/earth/shieldedstaking/v1/moves/{key}`). Query/Redelegation is removed.

Invariant 9 (redelegations) now checks 1..4,096 entries and the moves'
shares per unmatured entry; invariant 10 (new): every open move belongs to
an entry at its height, its retained is its row's (or its credit with no
row), the debt tree's leaves, index and rows agree, no slash is left
watched.

`redelegate` event: `minted` becomes `credited`; adds `move_key`,
`move_time`. The stake tree records its empty root at the first block (a
first delegation pads its input, so it proves against an anchor before any
note exists).

### 20.8 Wallet format

- **Quote** (delegate): Query/Validator for B and S (the live rate);
  derth = floor(amount x S / B) less a margin for the rate's drift until
  the tx lands (the per-block rewards over the backing, times the blocks
  you allow: ~10 ppm covers minutes on a chain with real stake), or amount
  exactly while S = 0. Redelegate: value = amount x rate_src, dst_derth =
  floor(value x S_dst / B_dst) less the margin of both rates. A refused
  quote costs nothing (refused in the ante).
- **Delegate**: lane A spends your derth/<v> note (merge) or pads (a fresh
  rho, position 0, its nullifier H(TAG_SNF, nk, rho, 0)); output = old +
  derth (a labelled input keeps its label and exposure); v_in = derth.
- **Undelegate / Lock**: the change, or a zero note (amount 0, fresh rho/
  rcm) when nothing is left; a labelled note may only release its
  unexposed part (or clear first).
- **Redelegate**: lane A as an undelegation; lane B spends your
  unlabelled derth/<dst> note (or pads when you have none or yours is
  labelled), cr_v_in = dst_derth, cr_move_time = move_time = the latest
  block's time. Your new dst note is labelled (move_key = the lane-B
  nullifier you published, move_time, exposed = dst_derth).
- **Unlock**: lane A merges the position's derth into your note there.
- **Clearing**: after move_time + window (Query/DebtTree: window_seconds,
  clear_before), any lane-A proof may clear: clear_before from the query,
  debt_root = its root, the witness from the rows (rebuild with zk/debt
  from Query/DebtTree's rows or the `shieldedstaking_debt_row` events; a
  move without a row: its low leaf). The note then holds amount - exposed +
  retained.
- **Ciphertext**: 201 bytes, epk || AEAD(0x04 || asset || amount || rho ||
  rcm || move_key || move_time || exposed) || tag.
- **Votes**: up to 2 notes; debt_root = the current root; a labelled
  note's value per the debt tree; keep spent notes' openings while a
  proposal snapshotted before their spend is open.
- **Discovery**: every stake note is a proof output with a wallet
  ciphertext (the blind stake ciphertext and chain-minted notes are gone).

### 20.9 Groundworks and audit 7 (module D) items

- Both books of a private redelegation, and the source as well as every
  destination of a slashed redelegation, are re-weighed at the end of the
  block.
- A position split naming an option pruned since: the validator's voter
  leaves it out when re-filed, and the export drops it from the splits (a
  split left with nothing is no split), as x/allocation's export drops it
  from its voters.
- D7-L2: Groundworks weight counts a self-bond only at a Bonded validator
  (x/allocation bondedWeight, PositionWeightSource.Weight); the operator is
  resynced when its validator bonds or starts unbonding; while its bond
  remains its vote is kept at weight zero, so the weight returns with no
  new vote.
- D7-L1: the gov module account is on the blocked list.

### 20.10 Measurements, genesis

stake 16,242 gates (2^14), vote 21,716 (2^15, within the bundled SRS);
action and membership unchanged. New stake and vote verifying keys;
genesis.json sha256 ffb269c5047e823b3f3aa27034767ff894c9fe76626703ccf42d0b1b321b6b59.
