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

Circuit `stake` (mobile 2253c48, 9,647 gates, 2^14, 19 tests): up to two
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
| Undelegate {amount} | spends, v_out = amount, change | mints owner-locked unbond claim (value at rate) to spc_mint |
| ClaimUnbonding {amount, pc, fee or fee_from_output} | spends claims, v_out = amount | mints ERTH (transferable) to pc in the pool; no bundle with fee_from_output |
| StakeVote {weight} | (superseded 2026-10-03, §15: vote proof, circuits/vote; nothing spent) | records vote |
| LockPosition {amount} | spends, v_out = amount, change; otag | position stores otag |
| Update/Unlock/PositionVote | spends nothing; otag must equal the position's | unlock mints derth to spc_mint |

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
(ANML leg), MsgClaimUnbonding. Pool notes use v2
(`EncryptBlindNote`: salt "earth.note.v2", pt 0x02||rho||rcm||memo64);
stake notes the chain mints (Delegate's derth, Undelegate's claim,
UnlockPosition's derth; StakeVote's re-mint until §15) use the new blind stake
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
parity; privacy_core `nf_leaf`, `vote_nf`, `assert_not_in_indexed`).

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
mint its stake note. Not capped: **bundle outputs.** The action circuit
range-checks `o_value` as a u64 (section 3), so a bundle may create a note
of up to 2^64-1. The chain cannot refuse it: the value is private, and the
public inputs (`cm_out`, a hash; `cv`, blinded by `rcv`) do not reveal it.
Refusing it needs a 63-bit range check in the circuit (new VK, wallets and
fixtures), deferred. Only a bundle's author can make such an output, from
value it spends, and wallets never build one; the note stays spendable by
the circuit, so a wallet that learns to hold it recovers it. The binding
argument (section 5) is unaffected: it needs only u64 values. A bundle's
public `Balance` value (u64) is a transparent amount leaving the pool, not a
note, and is not capped.

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
