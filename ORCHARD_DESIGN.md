# Earth privacy design (Orchard-style bundles, stake notes, slash debt)

The current specification of Earth's privacy layer: the shielded pool and its
bundles, the stake note tree, the circuits, and how every module uses them.
It states what the chain does now. How the design got here, and the
alternatives that were rejected, are in [HISTORY.md](HISTORY.md); the audit
rounds and their fixes in [AUDIT_HISTORY.md](AUDIT_HISTORY.md); release notes
in [CHANGELOG.md](CHANGELOG.md).

Principle throughout: **people are private; power and public money are
public.** Balances, owners and links between a person's txs are hidden;
module books, validator stake, pool reserves, allocation weights and every
amount the chain itself computes are public.

Genesis: `networks/genesis.json` sha256
`bae959be55a37e9c9c06c97207c91b92068202c6b20cacb51c30882c155f2779`, carrying
the action, stake, vote, membership and move verifying keys
(`networks/genesis/shielded-verifying-keys/*.vk.b64`) and the passport keys
(`networks/genesis/verifying-keys/`, 33 register circuits). `make genesis-check` and
`make privacy-vks-check` pin them.

Contents

1. Primitives (field, hashes, tags, curve, value bases)
2. Notes and bundles (action, bundle, sighash, binding signature,
   verification, soundness)
3. Trees and roots
4. Circuits
5. The private tx: ante, fees, gas, caps
6. Personhood and handles
7. Assembly
8. Staking
9. Allocation (Groundworks)
10. Dex
11. Genesis, invariants, exports
12. Wallet formats
13. Known limitations and audit notes

---

## 1. Primitives

### 1.1 Field and hash

All circuit values live in the BN254 scalar field `p`. `H` is Poseidon2
(`zk/poseidon2`, the circuit's `Poseidon2::hash`); the sponge absorbs the
input length, so hashes of different arity never collide. Field values are
encoded as exactly 32 big-endian bytes below the modulus
(`privacy.FieldFromBytes` refuses anything else, so byte-keyed sets have one
spelling per value).

- `U64(v)`: a u64 lifted into the field.
- `Bytes(b) = H(TAG_BYTES, len(b), c_0, …, c_k)`, `c_i` the 31-byte big-endian
  chunks; `Bytes(nil) = H(TAG_BYTES, 0)`.
- `AssetID(denom) = H(TAG_ASSET, len(denom), c_0, …)` over the denom's bytes,
  31-byte chunks. uerth's value is hard-coded in privacy_core as `ASSET_ERTH`.

### 1.2 Domain tags

Tags are ASCII strings read as big-endian integers (`"earth.id"` ->
`0x65617274682e6964`). Append only; never change or reuse a value.

| Tag | String | Use |
| --- | --- | --- |
| TAG_ID | `earth.id` | idc = H(TAG_ID, id_secret) |
| TAG_OWNER | `earth.owner` | owner_pk = H(TAG_OWNER, nk) |
| TAG_LEAF | `earth.leaf` | identity leaf |
| TAG_SN | `earth.sn` | membership scope nullifier |
| TAG_PC | `earth.pc` | pool note owner commitment |
| TAG_CM | `earth.cm` | pool note commitment |
| TAG_NF | `earth.nf` | pool note nullifier |
| TAG_VOTE | `earth.vote` | retired (never reuse) |
| TAG_REG | `earth.reg` | registration binding |
| TAG_ASSET | `earth.asset` | AssetID |
| TAG_STAKE | `earth.stake` | stake note commitment |
| TAG_SPC | `earth.spc` | stake note owner commitment |
| TAG_SNF | `earth.snf` | stake note nullifier |
| TAG_OTAG | `earth.otag` | position owner tag |
| TAG_SNFL | `earth.snfl` (`0x65617274682e736e666c`) | stake nullifier tree leaf |
| TAG_VNF | `earth.vnf` | stake vote nullifier |
| TAG_VPAD | `earth.vpad` (`0x65617274682e76706164`) | padding vote nullifier of an unused vote slot |
| TAG_SLABEL | `earth.slabel` | stake note slash label |
| TAG_DEBTL | `earth.debtl` | slash debt tree leaf |
| TAG_SIGNAL | `earth.signal` | Signal / sighash (chain and wallet only) |
| TAG_BYTES | `earth.bytes` | Bytes |
| TAG_SCOPE | `earth.scope` | membership scopes |
| TAG_AFFILIATE | `earth.affiliate` | registration referrer field |
| TAG_REFERRAL | `earth.referral` | referral note opening |
| TAG_GEN | `earth.gen` (`0x65617274682e67656e`) | value bases |
| TAG_CV_R | `earth.cv.r` (`0x65617274682e63762e72`) | value-commitment randomness base R |
| TAG_BSIG | `earth.bsig` | binding signature challenge |
| TAG_BUNDLE | `earth.bundle` | bundle digest |

Go: `zk/privacy/privacy.go` (all but the last four), `zk/orchard/curve.go`.

### 1.3 Curve

Grumpkin, Noir's embedded curve: `y² = x³ − 17` over `p`, prime order `n` (the
BN254 base modulus), cofactor 1. Go uses `gnark-crypto/ecc/grumpkin`; its
generator is Noir's `(1, sqrt(−16))`; infinity is `(0, 0)` on both sides.
Noir's default generator is never used.

    hash_to_point(tag, input):
        for ctr = 0, 1, ...:
            x = Poseidon2(tag, input, ctr)
            if x³ − 17 is a square in F_p:
                y = sqrt(x³ − 17), negated if y > (p−1)/2
                return (x, y)

    G_a = hash_to_point(TAG_GEN, AssetID(denom))     per-asset value base
    R   = hash_to_point(TAG_CV_R, 0)                 ctr 0:
          x = 0x07fc551d28471de4eb62cf956996e963f8a0877bb97afd963ad5f296973401ce
          y = 0x17bbea25d2f097a980174168aaffe0d61e97da2edeb3eec90c3a5e8a3b9328a4
    G_uerth: ctr 0, x = 0x16cbb7…eae68;  G_uanml: ctr 3, x = 0x18a0d7…587a5

The chain and wallets use the least ctr (`orchard.ValueBase`, memoized; no
registry state). The circuit takes `(ctr, y)` as a Brillig hint and
constrains `x = H(TAG_GEN, asset, ctr)`, `y² = x³ − 17` and `y ≤ (p−1)/2`
(the canonical sign: section 2.6).

---

## 2. Notes and bundles

### 2.1 Pool notes

    owner_pk = H(TAG_OWNER, nk)
    pc       = H(TAG_PC, owner_pk, rho, rcm)
    cm       = H(TAG_CM, AssetID(denom), value, pc)
    nf       = H(TAG_NF, nk, rho, position)

Every note value is at most `MaxNoteValue = 2^63 − 1`: every wallet (Android
Long, iOS Int64, web) holds values only up to it, so the circuits range-check
every note value to 63 bits (`NOTE_VALUE_BITS = 63` in `note_cm`) and every
chain mint refuses a larger one (`types.FitsNote`). The nullifier includes
the position, so two notes with the same rho still have distinct nullifiers.

### 2.2 Action circuit (`circuits/action`)

One spend and one output, either may be a dummy.

    private  nk, s_asset, s_value (u64), s_rho, s_rcm, s_pos (u32), s_path[32],
             o_asset, o_value (u64), o_pc, rcv
    public   anchor, nf, cm_out, cv_x, cv_y, sighash          (6, in this order)

    owner_pk = H(TAG_OWNER, nk);  pc = H(TAG_PC, owner_pk, s_rho, s_rcm)
    s_value, o_value < 2^63
    cm_in    = H(TAG_CM, s_asset, s_value, pc)
    s_value ≠ 0  ⇒  merkle_root(cm_in, s_pos, s_path) == anchor
    nf       = H(TAG_NF, nk, s_rho, s_pos)               always (dummies too)
    cm_out   = H(TAG_CM, o_asset, o_value, o_pc)
    cv       = MSM([G(s_asset), −G(o_asset), R], [s_value, o_value, rcv])
    bind(sighash)

`rcv` is a Field (< p < n) lifted with `EmbeddedCurveScalar::from_field`; the
bias against uniform mod n is ~2^-127. Spend and output may be different
assets: each term uses its own asset's canonical base.

Dummies: a dummy spend has `s_value = 0` (path not checked, so `anchor`,
`s_asset`, `s_pos` are free; the wallet uses a fresh random rho, and may use a
throwaway nk, so nf is unique; the chain records it like any nullifier). A
dummy output has `o_value = 0`, a random `o_pc` and a ciphertext to a
throwaway key. Neither needs a flag: a value-0 term adds nothing to cv.

Known lint: nargo's "Brillig call isn't properly covered" fires on the two
hint calls; a false positive (every hint output enters a constraint: ctr feeds
the Poseidon2 defining x; y the curve equation and the sign check).

### 2.3 Bundle

    Action  { anchor, nullifier, commitment, cv (x‖y, 64 B), ciphertext (217 B), proof (14,656 B) }
    Balance { denom, amount: u64 > 0 }                         value LEAVING the pool
    Bundle  { actions[2..32], balances[], binding_sig (96 B) }

Stateless rules (`types.Bundle.ValidateBasic`, `orchard.Bundle.ValidateBasic`):

- `MinActionsPerBundle = 2` .. `orchard.MaxActions = 32` actions (padding with
  dummies); param `max_actions_per_bundle` (2..32, default 16) caps lower.
- Every field canonical; every cv a Grumpkin point; nullifiers distinct.
- **One anchor per bundle**: every action proves against the same anchor
  (dummies included), so dummies cannot be told from real spends by anchor.
- Every action ciphertext is exactly `NoteCiphertextBytes` = 217 (a v1 note
  ciphertext; dummies too).
- Every proof exactly `ProofBytes` = 14,656.
- Balances positive, one per denom, valid denoms, at most `2 × actions` (a
  balance of an asset no action touches has no discrete log a signer could
  know).
- Binding signature exactly 96 bytes.
- `MaxBundlesPerMsg = 1`: every private msg carries exactly one bundle.
- Gas params are bounded together: `bundle_gas + max_actions_per_bundle x
  (proof_verification_gas + 2 x note_gas)` must be at most
  `MaxBundleShapeGas` (60M of the 100M block), so the largest msg the
  params allow can always be included.

Only positive balances exist: value enters the pool through `MsgShield` and
chain mints (`MintNote`), never through a bundle. Only registered denoms may
carry a balance.

### 2.4 Digest and sighash

    digest(bundle) = H(TAG_BUNDLE, N,
                       anchor_0, nf_0, cm_0, cvx_0, cvy_0, Bytes(ct_0), …,
                       M, AssetID(denom_0), amount_0, …)

    Signal(type, chain, fields…) = H(TAG_SIGNAL, Bytes(type), Bytes(chain), fields…)

    sighash = Signal(msg_type_url, chain_id,
                     K, digest(bundle_0), …, digest(bundle_K−1),
                     Bytes(memo), timeout_height, gas_limit,
                     msg fields…)

N, M, K are counts (K keeps a digest from posing as a field). `memo` is the tx
body's memo (UTF-8 bytes; `Bytes("")` for none), `timeout_height` the body's,
`gas_limit` the auth info's fee gas limit, both u64 (0 when unset)
(`orchard.Sighash`, `types.SighashOf(ctx, msg, ac)`; the private ante records
the tx fields with `types.WithTxFields`). Nothing left in an unsigned private
tx can change without changing the sighash.

Every action proof binds the sighash; every binding signature signs it; every
other proof in the msg (membership, passport via its signal, stake, vote)
binds it as its signal. The per-msg fields are listed with each msg below.

### 2.5 Binding signature

    bvk = Σ_i cv_i − Σ_a amount_a · G_a
    bsk = Σ_i rcv_i  mod n                              (wallet)
    sig = Rn.x ‖ Rn.y ‖ s   (96 bytes)
          k  = SHA-512(bsk ‖ sighash ‖ 32 random bytes) mod n,   Rn = k·R
          e  = Poseidon2(TAG_BSIG, Rn.x, Rn.y, bvk.x, bvk.y, sighash)
          s  = k + e·bsk mod n
    verify: Rn on curve and ≠ O, s < n, bvk ≠ O, s·R == Rn + e·bvk

`bvk = O` is refused (with bsk = 0 anyone could re-sign another sighash).
Go: `zk/orchard/binding.go`.

### 2.6 Soundness

Claim: if every action proof and the binding signature verify, then for every
asset a, Σ spent notes of a = Σ created notes of a + public amount_a.

1. **Notes.** A real spend (value ≠ 0) opens a commitment in the tree under
   the anchor; its nullifier is a function of the note (nk, rho, position),
   so it is spent once: duplicates are refused within a bundle and across
   txs (nullifier set). A value-0 spend contributes nothing.
2. **Value.** Each `cv_i = Σ_B α_iB·B + rcv_i·R` over bases
   B = hash_to_point(TAG_GEN, a, c) with α = +s_value for the spend's base and
   −o_value for the output's. A valid signature gives knowledge of bsk with
   bvk = bsk·R, hence Σ_B (Σ_i α_iB − pub_B)·B + (Σ rcv_i − bsk)·R = O. The
   bases and R are independent random-oracle points, so under discrete log
   every coefficient is 0 mod n. Per base |Σ| ≤ MaxActions × 2 × (2^63−1) +
   (2^64−1) < 2^70 ≪ n, so they are 0 over the integers: per base, spent =
   created + public. `MaxActions = 32` is part of this argument.
3. **Per base ⇒ per asset.** A base is bound to one asset (hashed into x; the
   circuit uses the same asset for the commitment). A non-least ctr is
   harmless: its base carries no public balance and no known relation to
   anything, so value under it cancels only against value under it.
4. **The −G case.** Try-and-increment yields two points per (asset, ctr),
   (x, y) and (x, −y) = −G_a, with the known relation G + (−G) = O; outputting
   v under G_a and v under −G_a would net to zero and the binding signature
   would verify (Go: `TestNegatedBaseInflation`). The circuit's
   `y ≤ (p−1)/2` leaves one of the two (nargo tests
   `test_negated_output_base_rejected`, `test_negated_spend_base_rejected`).
   **The chain cannot detect this; the circuit check is the only defence.**
   Grumpkin has prime order (no torsion); its GLV endomorphism maps G_a to
   λ·G_a (canonicality preserved); a base equal to another's image needs a
   targeted Poseidon2 preimage or a ~2^127 birthday search.
5. **Non-malleability.** Every proof binds the sighash, which binds every
   field of every bundle, the msg and the tx fields. A relayer can neither
   re-sign (needs bsk) nor move proofs; dropping or adding actions breaks bvk.
6. **Value bound.** No circuit opens or creates a commitment to a value above
   2^63−1 (`note_cm` and `stake_cm` range-check before hashing): the output
   bound is what stops a bundle making a note no wallet sees; the input bound
   makes the argument need no induction over the tree's history.
   `TestOrchardOutputAboveNoteMax` (zk/ultrahonk): a balanced bundle with a
   2^63 output does not verify. Not notes, not bounded: the stake circuit's
   public `v_in`/`v_out` and a bundle's public Balance (u64), which the chain
   sees.

---

## 3. Trees and roots

All note trees are append-only depth-32 Poseidon2 Merkle trees
(`zk/merkle`, `Depth = 32`).

### 3.1 Pool note tree (x/shielded)

Pool notes (`MsgShield`, bundle outputs, chain mints). Its root is recorded at
the end of every block that changed it; an anchor is valid for
`root_window_seconds` (default 14 days, at most a year) after it stopped being
the latest. CheckTx (and recheck) refuses an anchor that lapses within
`AnchorCheckTxMarginSeconds` (120 s) of the last block's time (pick a newer
one); PrepareProposal leaves out a private tx certain to fail on a lapsed
anchor. At most `RootPruneLimit` (20) roots are pruned a block.

### 3.2 Identity tree (x/personhood)

Identity leaves `H(TAG_LEAF, idc, dsc_key, country, activated_at,
predecessor_at)` (country = `CountryField`: the two ASCII bytes of an ISO
3166-1 alpha-2 code, 0 for unknown; predecessor_at the switch or re-entry that
created the leaf, 0 for a passport never registered). A switch or expiry
zeroes the old leaf. Membership proofs prove against a recent identity root.

**Succession leaves.** When a passport registers to `idc_new` and its last
registration (live, or lapsed: `PassportsSeen` keeps the last idc) was to
another `idc_old`, the chain appends `H(TAG_SUCC, idc_old, idc_new)` (`TAG_SUCC`
= "earth.succ") to the identity tree right after the new identity leaf. It is
never zeroed and is exported in genesis (`successions`, with `passports`
carrying each passport's last idc). Every idc is registered at most once, by
one passport (6.1, `used_idcs`), so a succession always links two identities
of one passport and an identity appears in at most one chain, once. Another tag and arity than an identity
leaf, so no membership proof can use one. Both idcs are already public in
the passport's registration records, so the leaf discloses nothing new; a
move proof shows one is in the tree without saying which.

### 3.3 Stake note tree (x/shieldedstaking)

    spc    = H(TAG_SPC, owner_pk, rho, rcm)
    label  = 0, or H(TAG_SLABEL, move_key, move_time, exposed)
    cm     = H(TAG_STAKE, AssetID(derth/<valoper>), amount, spc, label)
    nf     = H(TAG_SNF, nk, rho, position)
    otag   = H(TAG_OTAG, owner_pk, salt)

`derth/<valoper>` is not a coin and not a pool asset (`ExcludeAssetPrefix`:
shielded-only for every transparent path; the dex refuses it). Stake notes are
**owner-locked**: the stake circuit outputs only stake pcs of the spender's
own owner_pk. **The chain mints no stake note**: every stake note is an
output of a stake proof (section 8). Root window `stake_root_window_seconds`
(default 14 days, from when a root stopped being the latest, as in 3.1). The tree records its empty root at the first block (a
first delegation pads its input and proves against an anchor before any note
exists). Roots and size are recorded at the end of every block that changed
them (`StakeLatestRoot`).

### 3.4 Stake nullifier tree (`zk/indexed`, keeper `nf_tree.go`)

An indexed (sorted, Aztec-style) tree on a depth-32 Poseidon2 Merkle tree,
holding every spent stake nullifier:

    leaf_i     = H(TAG_SNFL, value, next_value, next_index)
    sentinel   = leaf 0 = (0, smallest value, its index); written by the first insert
    empty slot = 0 (no tagged hash is 0, so it is never a leaf)
    next_value = 0 (next_index 0) for the largest value

Insert nf (nonzero, absent): low = the leaf with the largest value < nf (the
sentinel if none); append (nf, low.next_value, low.next_index) at the next
index n; set low to (low.value, nf, n). nf is absent iff some leaf has
value < nf and (nf < next_value or next_value = 0). Values compare as
integers (32-byte big-endian encodings sort the same way).

Empty root (sentinel alone, size 0 or 1): `indexed.EmptyRoot` =
`0x18f5a2d2d3273f584793e90ac9bf77abf0ff2a05a101cd5943eaf7bbd0bd5b10`.

State: value -> leaf index (`StakeNullifiers`), leaf index -> value
(`StakeNfValues`, insertion order), nodes, size (sentinel included), and the
root and size recorded at the end of every block that changed it
(`StakeNfLatestRoot/Size`). No root window: only snapshots prove against it.
Each insert rewrites two paths.

### 3.5 Slash debt tree (`zk/debt`, keeper `debt_tree.go`)

An indexed depth-32 Poseidon2 tree with one row per **slashed** redelegation
move (section 8.7):

    leaf = H(TAG_DEBTL, key, next_key, next_index, retained)    sentinel leaf 0

A move with a row is worth its row's `retained`; a move absent (low leaf
below it, successor above or none) is worth its whole exposure. Rows are
written only in BeginBlock (so the root is stable through a block's txs) and
never removed. Proofs use the **current** root only. Empty root (`debt.EmptyRoot`)
= `0x0cea3d3e26cd2710109d7cbff5bf48570ba54332f812d538893f0958007f6903`.

### 3.6 Proposal snapshots

When an x/gov proposal enters voting, x/shieldedstaking takes a
`ProposalSnapshot`: the stake note tree's latest recorded root and size
(`root`, `tree_size`) and the nullifier tree's (`nf_root`, `nf_size`), each as
of the end of the last block that changed it, so they describe one moment. A
note under `root` was unspent then iff its nullifier is absent under
`nf_root`. A snapshot taken after a failed root recording (`RootsStale`) takes
no roots: no note votes on that proposal (position votes still count; with no
note votes none is counted twice). A proposal x/gov cancelled takes no stake
or position votes. Per-validator supply is
checkpointed lazily (`SupplyCheckpoints`) for the tally.

---

## 4. Circuits

All proofs are bb v5.0.0 UltraHonk ZK (noir-recursive), exactly **14,656
bytes** (458 field elements) for every circuit; `zk/ultrahonk.Verify` and
every stateless check (`CheckProofLength`) require exactly that length. Gate
counts (`bb gates`, nargo 1.0.0-beta.22):

| Circuit | Gates | Dyadic | Public inputs (in order) |
| --- | --- | --- | --- |
| action | 8,098 | 2^13 | anchor, nf, cm_out, cv_x, cv_y, sighash |
| stake | 16,242 | 2^14 | anchor, asset, nf_0, nf_1, cm_out, v_in, v_out, clear_before, debt_root, cr_asset, cr_nf, cr_cm, cr_v_in, cr_move_time, otag, sighash |
| vote | 22,011 | 2^15 | note_root, nf_root, debt_root, asset, weight, proposal_id, vnf_0, vnf_1, sighash |
| membership | 5,659 | 2^13 | root, scope, nullifier, signal, excluded_dsc, excluded_country, max_activation, max_predecessor |
| move | 8,362 | 2^14 | root, scope, old_nullifier, new_nullifier, signal |
| passport (33 `lean_poa_*` variants) | 138,556 – 659,250 | 2^18 – 2^20 | current_date, address, nullifier, dsc_key: positions are params; `address` carries the registration binding, `current_date` is pinned to block time |

The vote circuit fits the bundled SRS (2^15 + 1 points); the prover's SRS
hint must be at least the circuit's dyadic size. Action has 94 gates of
headroom under 2^13. nargo tests: action 41, stake 52, vote 50 (mobile
`circuits/`), plus privacy_core's Go parity vectors.

Measured (Apple M2): action prove 0.34 s single-thread, 0.16 s on 8 threads;
Go verify 4.35 ms per action proof; binding check 0.12 ms per bundle.

### 4.1 Stake circuit (`circuits/stake`)

Two lanes, one owner (nk), every input under `anchor`; every note amount
≤ 2^63−1.

- **Lane A** (`asset`): up to two inputs, one output; `v_in` credited,
  `v_out` leaving. At most one labelled input, which either
  - **keeps** its label: the output carries the same label and exposure, and
    `in_0 + in_1 − exposed + v_in == out − exposed + v_out` (only unexposed
    value moves); or
  - **clears** it, once its window has closed (`move_time < clear_before`):
    the exposure is worth `retained` (`debt_retained` against `debt_root`),
    and the output is unlabelled:
    `in_0 + in_1 − exposed + retained + v_in == out + v_out`.
- **Lane B, the credit lane** (`cr_asset`): one unlabelled input (or
  padding), one output = `cr_in + cr_v_in`. With `cr_move_time ≠ 0` (a
  redelegation) the output is labelled (`move_key = cr_nf`, the lane's own
  nullifier; `move_time = cr_move_time`; `exposed = cr_v_in`).
- **Padding.** An amount-0 input publishes 0 or its own would-be nullifier
  (still derived from nk); an amount-0 output publishes 0 or the zero note's
  commitment.
- `otag = H(TAG_OTAG, owner_pk, salt)` of the same owner; binds sighash.

The chain fixes per msg the assets, `v_in`, `v_out`, `cr_v_in`,
`cr_move_time`, `debt_root`, `clear_before` and which slots must be zero.
`clear` is a witness: the circuit checks `clear_before` and `debt_root` only
when it clears; the chain checks them on every proof (section 8.2).

### 4.2 Vote circuit (`circuits/vote`)

`MAX_NOTES = 2` slots, one nk (one owner):

    private  nk; per slot i: amount_i, rho_i, rcm_i, pos_i, path_i[32], label fields,
             low_value_i, low_next_value_i, low_next_index_i, low_index_i, low_path_i[32],
             debt witness
    public   note_root, nf_root, debt_root, asset, weight (u64), proposal_id (u64),
             vnf[0..1], sighash

    slot used (amount_i ≠ 0):
      cm_i = H(TAG_STAKE, asset, amount_i, H(TAG_SPC, H(TAG_OWNER, nk), rho_i, rcm_i), label_i)
      merkle_root(cm_i, pos_i, path_i) == note_root
      nf_i = H(TAG_SNF, nk, rho_i, pos_i) absent under nf_root (low leaf, as 3.4)
      vnf_i = H(TAG_VNF, nk, rho_i, pos_i, proposal_id)
      value_i = amount_i, or for a labelled note amount_i − exposed + retained
                (debt tree under debt_root, the CURRENT root)
    slot unused (amount_i = 0): vnf_i = H(TAG_VPAD, nk, r_i, proposal_id)   (r_i = rho_i, fresh random)
    0 < weight ≤ Σ value_i;  amounts < 2^63
    bind(sighash)

**Padding.** Every vote publishes two vote nullifiers. An unused slot's
padding nullifier is a Poseidon2 output like a real one, so without nk and
r it cannot be told apart: the number of notes voted is hidden, as is which
slot is padding (wallets place it at random). It never equals any note's
vote nullifier (another tag, and arity 4 against 5: the sponge absorbs the
length), so it can neither take a real vnf nor block one; a prover cannot
aim it at a chosen value (a preimage); and once used on a proposal it is
recorded like a real one, so neither a replayed vote nor another vote
reusing it is accepted. At least one slot is a note (weight > 0 ≤ Σ
values).

### 4.3 Membership circuit

Proves an identity leaf under `root` (the chain checks it is a recent
identity anchor), outputs `nullifier = H(TAG_SN, id_secret, scope)`, binds
`signal`, and excludes a DSC and/or a country (`excluded_dsc`,
`excluded_country`; 0 excludes nothing) and identities activated after
`max_activation` or whose predecessor is after `max_predecessor` (u64;
`NoBound` when unused).

Scopes (`H(TAG_SCOPE, Bytes(kind), args…)`):

| Scope | Args | Used by |
| --- | --- | --- |
| `claim` | UTC day | MsgClaimAnml |
| `caretaker` | — | MsgSetCaretaker, MsgMoveCaretaker (move proof) |
| `handle` | — | MsgBindHandle, MsgMoveHandle (move proof) |
| `proposal` | proposal_id, round (0, or 1 after the chamber demoted an expedited proposal) | MsgVoteProposal |
| `removal` | ballot_id | MsgVoteRemoval |
| `propose_removal` | option_id, UTC day | MsgProposeRemoval (nullifier not recorded) |

The signal of every membership proof is the msg's sighash
(`SignalOf = SighashOf`).

### 4.4 Golden vectors (Go = Noir parity)

- `zk/indexed TestNoirParity` (= Noir `test_go_parity`):
  `nf_leaf(1, 2, 3)` = `0x0cdc3a81748c6389efaa3a6c29b7f4609a8e9f860230b70413e8bef512978276`;
  `vote_nf(0x5eed, 0xa1, 1, 7)` = `0x1ada84dad3e6afde3f370e97edf4df2ee4eeb6b1400d5c5f41882552f578ba2f`.
  `vote_pad_nf(0x5eed, 0x77, 7)` = `0x08d195db55c5c0006ae0d2e8ee33df8cd5286226124074ad37c1d5ebe74b6235`.
- `zk/debt TestNoirParity` (= `test_go_parity_debt`):
  `DebtLeaf(1, 2, 3, 4)` = `0x0b28cc858d976ddad0ede75ca9538f9b5ab36538964f6e241b8e89be2711e82a`;
  `StakeLabel(0x4d4b, 1000, 200)` = `0x2dfbc154973d1d3ec6e03137ba41b5c2cf5119f66cc69e3e58c033a77c80a881`;
  `StakeCM(1, 2, 3, 4)` = `0x0ffc538b4162732774bd5026e7a07bd00d2fe406af55231fa0c255c321ad4232`;
  debt EmptyRoot = `0x0cea3d3e…6903` (3.5).
- `zk/privacy TestRegistrationBindingPinned`: chain_id "earth-1", idc 1,
  pc_anml 2, ciphertext_anml "anml", pc_erth 3, ciphertext_erth "erth",
  affiliate 0 -> `148b3513a501b6ff9c02314f355cb83fb544e22b2a9df79552fe49c944424159`.
- `zk/orchard`: R, G_uerth, G_uanml (1.3) and point-arithmetic vectors, pinned
  in privacy_core.

---

## 5. The private tx

### 5.1 Shape

A private tx is unsigned: exactly one `PrivateMsg`, no signatures, no signer
infos (`ante.NewRouter`; a private msg cannot ride along with signed msgs).
Its authorization is its bundles (and its module's proofs), its replay
protection the nullifiers. Refused: `timeout_timestamp` (use
`timeout_height`), unordered, extension options, a tip, a payer, a granter;
the declared fee must equal the msg's fee in uerth; the tx bytes must be the
canonical encoding of what they decode to (one msg, one tx hash).

### 5.2 Ante order (`x/shielded/ante`, `PrivateMsgDecorator`)

1. Record the tx fields; refuse a bundle over `max_actions_per_bundle`;
   charge `PrivateMsgGas` (`bundle_gas` per bundle + `proof_verification_gas
   + 2 × note_gas` per action) plus the action's fixed gas, before anything
   else. Refuse a `gas_limit` above `PrivateGasCeilingFactor` (5) × the gas
   used ("exceeds what this private tx uses"); wallets declare simulated gas
   + 10%.
2. In FinalizeBlock, admit the actions under `max_private_actions_per_block`
   (counted in the ante's own writes: only txs that pass their ante count).
3. Fee floor: fee ≥ `min_fee` always, and ≥ the node's min gas price × gas in
   CheckTx.
4. State checks, cheapest first: anchors, nullifiers, assets registered, room
   in the tree (outputs + 1), the release map (5.3), then the action's own
   (`CheckPrivateAction`).
5. Verify: the action's own proofs, then every binding signature, then every
   action proof. In a block (and simulate) proofs run in parallel and the
   first failure in bundle/action order is reported (`orchard.VerifyProofs`,
   deterministic). In CheckTx they run one at a time and stop at the first
   failure (`VerifyProofsSequential`), and proofs seen to verify are cached
   (`CheckTxProofCacheSize` 8,192). Skipped on recheck; charged but not
   required in simulate.
6. Spend the nullifiers, append the outputs, pay the fee to fee_collector
   (x/earth burns half, rounded up: 5.3), pay an unshield's receiver, authorize the
   msg and its action (keyed by SHA-256 of the msg's proto bytes:
   `AuthorizedAction/Result/Positions`).
7. Run the action here if its handler must be atomic with the spend
   (`PrivateActionExecutor.ExecutesInAnte`: dex swaps, deposits and LP
   withdrawals, MsgRedelegate). A refusal there fails the tx in the ante: nothing spent,
   no fee.

The ante's writes and events persist when the msg then fails: a failed
private tx's result still carries its nullifier, note and fee events.
**Indexers must read note, nullifier and fee events from failed private
txs.** Every module handler must refuse unless `AuthorizedAction` returns
what CheckPrivateAction prepared for this very msg, and re-checks state it
depends on.

Cost of junk: a tx failing CheckTx pays nothing and a binding signature is no
filter, so CheckTx does the cheap checks first and costs a node at most one
unseen proof verification per junk tx. In a block every proof is paid for
first: the fixed private gas is consumed before any proof and counts toward
max_gas even when the ante fails, so block gas bounds a block's verification
work. bb's per-proof "verification failed" stderr line is silenced
(bb_log_level 3; `BB_VERBOSE` keeps it). Public nodes should rate-limit
CheckTx per peer (sentries, p2p and RPC limits).

Priority: fee / gas, capped at MaxInt64.

### 5.3 Fees and the release map

**One fee rule.** Every private msg pays its fee out of its bundles' uerth
balance: fee = the uerth balance less the uerth the msg itself moves
(`types.FeeAfter`). The fee must be positive. What a msg moves is explicit
where it is uerth: `MsgDelegate.amount`, `MsgNoteSwap.amount_in` when
`denom_in` is uerth, `MsgAddLiquidityShielded.erth_amount`; every other msg
pays its whole uerth balance. **MsgSend** names its fee: its uerth beyond the
fee, and every other denom, is unshielded to its receiver.

**Release map** (`types.Remainders`, `checkReleaseMap`, refused before
anything is spent): every balance beyond the fee must have exactly one
destination.

- MsgSend: every remainder (all denoms) goes to `receiver`, paid in the ante
  (atomic with the spend); a receiver is named exactly when the balances
  exceed the fee. The receiver must be able to take each coin.
- A msg with an action handler: its remainders must be exactly the denoms
  the handler declares (`ReleasedDenoms`), each send-enabled; the handler
  takes the whole remainder once with `ReleaseToModule(ctx, msg, denom,
  module)`. Delegation: uerth. Swap: its denom_in. LP deposit: both legs. LP
  withdrawal: its pool's `dexlp/<pool>` shares. Everything else: nothing.
- Any other msg releases nothing beyond its fee.

**Pool self-dealing.** `MintNote` and `ReleaseToModule` refuse the pool's own
module as the other side; the pool's module account has no Minter or Burner
permission.

**SendEnabled.** A send-disabled denom is refused at every pool edge: shield,
unshield, a module release (in the ante's release-map check) and a module
mint into the pool. Notes shielded before the switch still move privately.

**Turnstile.** Per denom, `ShieldedIn − ShieldedOut == module balance`
(releases and unshields count out; shields and mints count in).

**Fee split (x/earth `SplitCollectedFees`).** Yes, half of every fee is
burned. At each EndBlock x/earth takes what fee_collector holds (that
block's tx fees only: the emission is minted into it and swept by
x/distribution in BeginBlock) and, per denom, burns `ceil(fee / 2)` and
leaves `floor(fee / 2)`: an odd unit is burned, burned + paid = collected
exactly. The paid half is swept by x/distribution at the next BeginBlock
under the standard rules with `community_tax` 0: to validators by voting
power, then commission and delegators (the module's books' rewards W,
operators' reward escrows, 8.8). A fixed shape, not a parameter. Every fee
lands there: transparent tx fees and every private tx's fee (`payFee`).
Event `gas_fees_split {burned, to_validators}`; the burn is recorded under
source `gas_fees`. (The dex swap fee is a separate 50/50 split, `splitFee`.)

### 5.4 x/shielded params

| Param | Default | Bounds |
| --- | --- | --- |
| `min_fee` | 1,000 uerth | |
| `proof_verification_gas` | 2,000,000 | 1..10,000,000 |
| `note_gas` | 150,000 | 1..1,000,000 |
| `bundle_gas` | 100,000 | 1..1,000,000 |
| `root_window_seconds` | 14 days | 1 s..1 year |
| `max_actions_per_bundle` | 16 | 2..32 |
| `max_private_actions_per_block` | 32 | ≥ 2 × max_actions_per_bundle, ≤ 256 |
| `verifying_keys` | action (and any known circuit) | non-empty |

### 5.5 MsgSend and MsgShield

    MsgSend { bundle, receiver, fee }
    sighash fields: Bytes(receiver raw address bytes), fee

`receiver` and every staking valoper must be the lowercase canonical
bech32; no private msg carries any other address. An unshield of uerth pays
its fee from what it releases.

`MsgShield` (signed) moves coins into the pool as a note to a pc with a
required v2 ciphertext; refuses a value above 2^63−1 and a send-disabled
denom. The gas grant pays through it.

### 5.6 Chain mints

`MintNote` (module -> note, with a required 177-byte v2 blind ciphertext;
refuses a value above 2^63−1), `MintNoteSplit` (a chain-priced value above
2^63−1 minted as `ceil(v / (2^63−1))` notes to one pc and ciphertext, at most
`MaxSplitNotes` = 128, each its own position and `shielded_mint` event; the
owner checks each position's cm with that position's amount), `MintOpenNote`
(the referral note: no ciphertext; the event carries `owner_pk`, `rho`,
`rcm`). Chain mints have no cv: their value is public and enters through the
turnstile. Every note the chain mints to a hidden owner carries a ciphertext
supplied by the msg that asks for it, bound by that msg's sighash (or
signature) and emitted with the note.

Events: `shielded_note {position, commitment, ciphertext}`,
`shielded_nullifier {nullifier}`, `shielded_root {root, tree_size, height}`,
`shielded_shield`, `shielded_mint {…, amount, position, ciphertext; owner_pk,
rho, rcm for an open note}`, `shielded_unshield`, `shielded_spend_to_module`,
`shielded_fee`, `shielded_asset`.

### 5.7 Assets

`RegisterAsset` admits a denom (asset id `AssetID(denom)`); value bases are
derived. `dexlp/<pool>` is admitted on first use and pool-locked
(`RegisterPoolLockedPrefix`: its notes cannot be unshielded). `derth/` is
excluded (`ExcludeAssetPrefix`).

---

## 6. Personhood and handles

All personhood private msgs carry `Bundle fee = 1` (its only balance is the
uerth fee) and, except MsgRegister, a membership proof whose signal is the
sighash. Gas: `proof_verification_gas + writes × note_gas` with writes:
MsgClaimAnml 2, MsgSetCaretaker 4, MsgMoveCaretaker 6, MsgBindHandle 9,
MsgMoveHandle 4 (the two moves carry a move proof instead, priced the same);
MsgRegister: passport proof gas + DSC verification gas + 6 × note_gas (one
for a succession leaf).

| Msg | sighash fields |
| --- | --- |
| MsgRegister | idc, pc_anml, Bytes(ciphertext_anml), pc_erth, Bytes(ciphertext_erth), affiliate, Bytes(signature_algorithm), every public signal in order |
| MsgClaimAnml | day, pc, Bytes(ciphertext) |
| MsgSetCaretaker | (option_id, percent) per entry |
| MsgBindHandle | Bytes(handle), owner_pk, Bytes(ek_pub) (Bytes of nothing for a release) |
| MsgMoveCaretaker | (none) |
| MsgMoveHandle | Bytes(handle) |

### 6.1 Registration

The passport proof's `address` public input must equal

    RegistrationBinding = H(TAG_REG, Bytes(chain_id), idc, pc_anml, Bytes(ciphertext_anml),
                            pc_erth, Bytes(ciphertext_erth), affiliate)
    affiliate = 0, or H(TAG_AFFILIATE, Bytes(affiliate_handle))

so a registration cannot be replayed onto another network, and whoever relays
it cannot swap the notes, ciphertexts or referrer. The chain verifies the DSC
against the CSCA trust store, binds it to the proof's dsc_key, pins
current_date to block time and dedups on the passport nullifier.

**The registrant knows the identity's secret.** The register circuit takes
the identity secret as a private witness and outputs `idc = H(TAG_ID,
id_secret)` as its fifth public input (index 4); the chain requires it to
equal `MsgRegister.idc`, or refuses with `ErrBadPublicInputs`. The register
layout `[current_date, address, nullifier, dsc_key, idc]` is code
(`types.Register*Input`), not a param, and a proof with any other number of
public inputs is refused: a governance swap of the nullifier and idc
positions would otherwise make the fresh idc serve as the passport nullifier,
so one passport could register without limit (audit R3-C3).
This holds for a first registration, a re-entry and a switch alike. Without
it a holder could register her passport to an idc that someone else chose,
whose secret only they know (audit R2-B1): the succession the chain then
writes would let them move her handle or caretaker split to that identity,
from which she could never move it back.

**Fresh identities only.** An idc that has ever been registered, by any
passport, is refused (`ErrIdcUsed` 1130, in the ante, before the proof): the
chain keeps the set `UsedIdcs` (genesis `used_idcs`, field 23, which must
hold every registration's, passport's and succession's idc). So one idc is
never in two passports' chains, and a switch back to an earlier identity
(A → B → A) is refused: it used to carry A's moved-out marks onto the live
identity and strand a handle or split moved to B (audit R2-B2). Every
registration, switch and re-entry uses a fresh identity secret; a wallet
never re-registers a retired one. The set also cannot be used to block
someone: occupying an idc needs its secret.

**Register circuits.** `params.verifying_keys` maps a variant id (the msg's
`signature_algorithm`) to its key; genesis carries 33, one per DSC key
type, signature padding and hash profile (the mobile repo's
`circuits/variants.json` and `PASSPORT_COVERAGE.md`): RSA-2048/3072/4096
with PKCS#1 v1.5 or PSS and any exponent in [3, 2^17), ECDSA on P-224, P-256,
P-384, P-521 and brainpoolP224r1/256r1/384r1/512r1, each with the data-group,
eContent and signature hashes real passports carry (SHA-1 to SHA-512, mixed
where they mix). Every variant has the same five public inputs
(current_date, address, nullifier, dsc_key, idc), so the registration path
is one. The variant a prover names is not trusted for
anything: a proof under a hash or padding the passport does not use needs a
preimage or a forged signature, and the key type is bound by the
commitment. SHA-1 is accepted (issuer-formed inputs: a forgery needs a
second preimage); governance can drop those variants once the last SHA-1
passports expire (about 2027–2028).

**DSC commitment** (`certs.DscCommitmentOf`, `poa_core::dsc_commitment*`):
ECDSA Poseidon2(tag, x‖y) with tags P-256 1, P-384 2, P-521 3,
brainpoolP256r1 4, brainpoolP384r1 5, brainpoolP512r1 6, P-224 8,
brainpoolP224r1 9; RSA Poseidon2(10, e, modulus big-endian), the exponent
being a circuit witness (tag 7, RSA without it, is retired). A curve stated
as explicit ECParameters is that curve only when p, a, b, G and n all match
and the cofactor is 1. x/pki verifies the DSC's CSCA signature natively:
RSA PKCS#1 v1.5 (SHA-1 to SHA-512, SHA-224 included), RSA-PSS (MGF1 over
the message hash and trailer 1 only, else refused), ECDSA on any of those
curves or an explicit one.
`ciphertext_anml`/`ciphertext_erth` are required 177-byte v2 ciphertexts.
`MsgRegister` fields: fee 1, proof 2, public_signals 3, signature_algorithm 4,
dsc_der 5, idc 6, pc_anml 7, ciphertext_anml 8, pc_erth 9, ciphertext_erth 10,
affiliate_handle 15; 11–14 reserved.

A new registration (or one re-entering after its last lapsed) appends the
leaf, mints 1 ANML to pc_anml and the registrant's half of the reward to
pc_erth. **Referral note**: when it names `affiliate_handle` (a live handle;
else `ErrNoReferrer` 1121), the chain mints the referrer's half itself, with
`MintOpenNote`, to the address the handle resolves to at execution:

    pc  = PC(handle owner_pk, rho, rcm)
    rho = H(TAG_REFERRAL, nullifier, leaf_index, 0)
    rcm = H(TAG_REFERRAL, nullifier, leaf_index, 1)       (privacy.ReferralOpening)

(nullifier = the passport nullifier, leaf_index = the new identity leaf:
unique per registration). The note has no ciphertext; its `shielded_mint`
event carries `owner_pk`, `rho`, `rcm` (hex) beside `amount` and `position`;
the handle owner's wallet takes every mint whose owner_pk is its own and
checks the cm. Publishing the opening is safe: recipient and amount are
public, and the spend nullifier needs nk. With no referrer only the
registrant's half is drawn; the other half stays in the option's pool. A
handle that stops resolving between the ante and execution lands the
registration unreferred. The `register` event carries `handle`, `referral`
(amount) and `referral_position`.

A registration for a passport with a live registration is a **switch**: the
old leaf is zeroed, the new one appended, nothing paid. A switch to the same
idc is refused (`ErrRegistrationReplay` 1123); to any other idc registered
before, `ErrIdcUsed` 1130. A switch whose DSC differs
from the live registration's is refused (`ErrSwitchSignerMismatch` 1127); a
switch counts against its signer's daily cap (shared with registrations; the
network and country counters do not move; over it, 1113).

**Per-passport switch rule.** A switch's proof must carry a `current_date`
strictly later than the live registration's (`Registration.proof_date`;
otherwise `ErrSwitchProofStale` 1128). Proof dates are days pinned to block
time within the skew, so one passport switches at most once per day (after a
burst of at most the few dates the skew admits): a single holder cannot fill
the signer cap its signer's other holders share; filling it takes about a
cap's worth of distinct passports under that signer. The same rule makes a
registration proof that reached a block but failed (public, binding never
marked used) unreplayable once its holder has registered again that day or
later; a third party cannot force a switch with it. Wallets: prove a switch
on today's UTC date, and keep the id_secret of any broadcast registration
until its `current_date` has left the skew window (a failed one may still
land within it).

Known lag: membership checks the anchor, not the registration's expiry; an
expired registration proves until the expiry sweep zeroes its leaf (at least
budget/8 per block; normally the block it expires).

### 6.2 Handles

A registered human's name in a public directory, resolving to a shielded
address (owner_pk, ek_pub). One handle per human: claimed with a membership
proof in the handle scope.

- Live while now < expires_at (resolves; the owner renews by binding again:
  now + handle_lease_seconds). Renewal period while expires_at ≤ now <
  expires_at + handle_renewal_seconds: does not resolve, only the same
  nullifier may renew (under the claim bound), cannot be moved. Free after
  that (swept).
- **Renewal rule, exactly.** The holder (the handle-scope nullifier holding
  it) may renew **at any time** while it is live or in its renewal period
  (defaults: lease 365 days, renewal period 30 days; at most 2 years and 1
  year). There is no early window: a renewal is a MsgBindHandle of the same
  handle, and it sets `expires_at = block time + handle_lease_seconds`, so
  the new lease runs from the renewal, not from the old expiry (renewing
  early does not stack; the unused part is dropped). While the handle is
  live the renewal is unbounded (any `max_predecessor`); in the renewal
  period it is treated as a claim and needs the claim bound below, so an
  identity that switched away and back cannot revive it. After the renewal
  period the holder has no priority: the handle is free and anyone may
  claim it (`handleStatus`, `handleClaimable`, `handleStatement`,
  `applyBindHandle`; test `TestHandleLifecycle`).
- **Claim bound**: a claim, or a renewal or change of a handle that is not
  live, needs `max_predecessor < now − the longest handle lease ever in force
  − ActivationMarginSeconds (86,400)`, so anything a predecessor identity held
  has lapsed. A prover holding a live handle renews or changes it unbounded.
  One live handle per passport across identity switches.
- A change frees the old handle at once; a release frees it at once.
- **Moves (MsgMoveHandle, MsgMoveCaretaker).** A live handle, lease and
  all (or a live caretaker split, expiry and percentages), passes from an
  identity to the identity that succeeded it under the same passport, and
  only there. The msg carries a **move proof** (circuits/move, section 6.6);
  the chain fixes its scope (handle or caretaker), takes the msg's sighash as
  its signal, requires a recent identity root, that `old_nullifier` holds the
  handle (live) or split (live), and that `new_nullifier` holds none and
  never moved one away; afterwards `old_nullifier` may never claim or cast
  again (`ErrHandleMovedOut` 1125, `ErrCaretakerMovedOut` 1126). A proof that
  does not verify: `ErrInvalidMove` 1129. Why this keeps one live handle (and
  one split) per passport, daily switchers included: a move needs the
  chain's succession leaf (old, new), which exists only between consecutive
  identities of one passport, and the successor's live leaf; each identity
  holds at most one; a moved-out identity takes no more; and claims and
  casts still need the predecessor bound. A move to another person's
  identity has no succession leaf, so no proof.
- **What a move guarantees, and what it does not (audit R2-B1).** A holder
  cannot make a buyer's identity her successor: registration proves
  knowledge of the idc's secret, and an idc is registered once (6.1). So
  every identity in a passport's chain is one whose secret the passport's
  holder had when she registered it, and whatever moves along the chain
  stays hers to move again: after her next switch she can move it on from
  any identity she held, with secrets she knows. A buyer gets no on-chain
  assurance. To receive a split or handle he needs the secret of her
  successor identity, which she keeps knowing (she can move it away after
  her next switch). Sharing a secret remains possible, but only as an
  unenforceable promise, as with any key. What remains is selling the
  passport itself: a seller who hands the buyer her passport data (DG1 and
  SOD, which NFC reads with the MRZ) and her old secret lets him register
  her passport to his own identity and move her split or handle there, for
  one lease. That is a sale of the personhood, name and document number
  included, which no passport-based scheme can stop; a register proof run
  as a multi-party computation (her passport, his id_secret) would do the
  same without showing him the data. Both are listed in 13.
- **A move is one step.** It goes from an identity to its immediate
  successor, and only while that successor is live (its leaf in a recent
  root). There is no succession (A, C) after A → B → C, and A → B needs B
  live, so a handle or split still at A after the next switch can no
  longer move: it lapses at its lease end. Wallets move before switching
  again, and warn (or block) a switch while the current identity's
  predecessor still holds a handle or split.
- `HandleEntry.owner` (Query/Handle, Query/Handles, field 6): the
  handle-scope nullifier holding it, 64 lowercase hex characters, "" for a
  handle never claimed. Events `handle_bound` and `handle_released` carry
  `owner`; `handle_moved` carries `owner` (new) and `previous_owner`
  (both the move proof's public nullifiers).
- Self-referral residual: a lapsed registrant still holding a live handle may
  re-register and name it, paying the referral half to themself; bounded by
  per-passport re-entry and the referral half.

### 6.3 Caretaker splits

`MsgSetCaretaker` files a split in the caretaker scope (a refresh replaces
it); a new split (one the prover does not hold live) needs
`max_predecessor < now − caretaker lease − activation margin`. A split past
its expiry that the sweep has not reached is not held. `MsgMoveCaretaker`
moves a live split to the successor identity (6.2, moves; event
`move_caretaker` with `nullifier` and `previous_nullifier`).

### 6.4 Lease bounds

`Query/LeaseBounds` (`/earth/personhood/v1/lease_bounds`): `block_time`,
`activation_margin_seconds`, `handle_lease_seconds` (the longest ever in
force), `handle_claim_bound`, `caretaker_lease_seconds` (including a held
longer lease after a cut), `caretaker_cast_bound`,
`caretaker_lease_hold_until`. Wallets compute max_predecessor from these,
never from Params.

### 6.5 Sweeps

Lapsed caretaker splits are swept first on their own limit. Then one
retirement budget per block is shared by four sweeps, in order: revoked-DSC
purge, registration expiry, used bindings, handles. The
purge gets the largest share; each later sweep is guaranteed budget/8 (at
least 1); a second round hands what is left to sweeps that used their whole
allowance. A registration a sweep cannot retire is passed over for
a day, then retried. A recurring identity root moves its by-time entry.

### 6.6 Move proofs (circuits/move)

Private witness: `old_secret`, `new_secret` (the two identity secrets),
`succession_index` and `succession_siblings` (the succession leaf's path),
`dsc_key`, `country`, `activated_at`, `predecessor_at`, `leaf_index`,
`siblings` (the successor's live identity leaf and its path). Public, in
order: `root, scope, old_nullifier, new_nullifier, signal`. Constraints:

    succession = H(TAG_SUCC, H(TAG_ID, old_secret), H(TAG_ID, new_secret))  in the tree at root
    H(TAG_LEAF, H(TAG_ID, new_secret), dsc_key, country, activated_at, predecessor_at)  in the tree at root
    old_nullifier = H(TAG_SN, old_secret, scope)
    new_nullifier = H(TAG_SN, new_secret, scope)
    signal bound (the msg's sighash: chain id, tx fields, fee bundle, handle)

Nothing else is revealed: not which leaves, not the idcs, no passport data.
The successor must still be live at a recent root, so a move is made while
the successor holds the passport's live registration (before it switches
again; one step only, 6.2).

**When to move (wallets).** The successor stays live until the passport's
next switch, so there is no need to move at once, and a move right after a
switch links them by timing: switches are rare public MsgRegister txs (both
idcs and the passport nullifier are on the registration record), and a
`handle_moved` or `move_caretaker` a few blocks later ties the handle, its
`owner_pk`, or the split to that passport for anyone who can compute the
passport nullifier. The move's anonymity set is the successions whose
successor is still live, small at first. So the wallet, once a switch has
landed, suggests waiting a random delay (hours to days) before moving and
lets the user choose when to move; it never sends a move on its own (a move
spends a fee, which only the user starts). It reminds the user to move
before switching again, since a move is one step. Replays: the sighash binds the
fee bundle (spent once) and the handle; the scope separates handle from
split moves; the old nullifier is moved out. nargo tests (12): the move,
one step of a chain, and refusals for another passport's identity,
backwards, skipping a successor, a zeroed successor, a succession leaf
passed off as the identity leaf, wrong old or new secret, another scope,
a recipient nullifier not the successor's, and a wrong root.

---

## 7. Assembly

The chamber: one live registration is one vote, cast anonymously with a
membership proof and a fee bundle (`Bundle fee`, whole uerth balance).

| Msg | scope | sighash fields | gas writes |
| --- | --- | --- | --- |
| MsgVoteProposal | proposal(id, round) | proposal_id, option | 2 |
| MsgProposeRemoval | propose_removal(option, day) | option_id | 4 |
| MsgVoteRemoval | removal(ballot_id) | option_id, option | 2 |

A proposal vote excludes the proposal's subjects (the signer or country it
revokes) and requires `max_predecessor = round opened − activation margin`
(an identity that replaced another after the round opened may not vote; one
never registered before votes even if it registered after). An expedited
proposal the chamber ratifies and x/gov demotes votes again in round 1 (new
nullifier scope; `Query/BallotInputs` reports it). Chamber votes pass the
circuit breaker. Bicameral rule: every gov proposal must also carry the
chamber, `yes × 3 ≥ (yes + no) × 2` of the human votes cast (three quarters
on the expedited track; `types.Approves`, `ApprovesExpedited`; integer
arithmetic, no quorum, a ballot with no votes does not carry). Removal
ballots use the two-thirds bar; the chamber alone can remove Groundworks
options.

---

## 8. Staking (x/shieldedstaking)

The module is the sole non-self delegator to x/staking. A validator's book:
`D` (module delegation), `W` (unwithdrawn rewards), `P` (queue: ERTH waiting
for the epoch end), `U` (pending undelegation); backing `B = D + W + P − U`,
derth supply `S`, live rate `B / S`. Every conversion is an integer floor
favouring the book. x/staking's MsgDelegate/MsgUndelegate/
MsgCancelUnbondingDelegation are refused except for an operator's own
self-bond, and MsgBeginRedelegate always (`ErrTransparentStaking` 1110: the
ante filter, top level and in authz MsgExec, and the staking hooks, which
nothing routes around).

Params: `epoch_seconds` (1 day), `stake_root_window_seconds` (14 days),
`min_position` (1 ERTH), `min_delegation` (1 ERTH).

### 8.1 One stake note per validator

The chain mints no stake note. Value the chain credits (a delegation's
derth, an unlocked position's, a redelegation's arrival) is a public `v_in`
the stake proof merges into the owner's existing note: one note per (owner,
validator). The credit follows the live rate, so **the wallet names the
credit and the chain checks the price**: the credited derth is a public input,
refused unless the value buys it at the live rate (`derth ≤ floor(amount × S
/ B)`, `= amount` while S = 0; `derth ≥ min_delegation`). What the value buys
beyond it stays in the book (every holder's rate). A quote the rate outran is
refused in the ante at no cost.

Padding: every note-moving msg must spend in lane A's first slot (the
owner's note or a padding nullifier: a first delegation looks like a top-up)
and create a note (the merged note, the change or a zero note: a full exit
looks like a partial one). No stake note's amount is ever public.

### 8.2 StakeProof and StakeFields

    StakeProof {proof 1, anchor 2, nullifiers 3 (exactly 2), owner_tag 7,
                commitment 9, ciphertext 10, credit_nullifier 11, credit_commitment 12,
                credit_ciphertext 13, clear_before 14, debt_root 15}
                reserved 4, 5, 6, 8 ("commitments", "ciphertexts", "spc_mint", "spc_ciphertext")

    StakeFields = anchor, nf_0, nf_1, cm, Bytes(ct), credit_nf, credit_cm, Bytes(credit_ct),
                  owner_tag, clear_before, debt_root        (absent ciphertext: Bytes of nothing)

Every staking msg's sighash binds the StakeFields first, then its own fields.
`ValidateBasic`: proof length, canonical fields, exactly two lane-A
nullifiers, non-zero nullifiers distinct, a ciphertext exactly for a non-zero
commitment and exactly **201 bytes** (wallet stake ciphertext; label fields
zero when unlabelled, so the length says nothing), `debt_root` zero when
`clear_before` is 0.

**Every stake proof names the current window** (`checkStakeClear`, in
CheckPrivateAction; refused with `ErrStakeTree` 1113): `clear_before` within
`[ClearBefore(now) − ClearBeforeSlackSeconds (3,600), ClearBefore(now)]`
and `debt_root` the current debt root, whether or not it clears a label, so a
proof that clears looks like every other. `ClearBefore(now)` = block time −
window (0 only while the block time is below the window, when clear_before
must be 0).

Owner tag salt: fresh random on every proof that does not act on a position
(delegate, undelegate, redelegate, restake); a lock takes a fresh salt that
stays the position's, and the position's update, vote and unlock reuse it. A
reused salt links txs.

### 8.3 Msgs

Every staking msg: `bundle` (fee: whole uerth balance, except MsgDelegate),
`fee` fields reserved. Stake proof lanes per msg:

| Msg (fields) | lane A | v_in / v_out | lane B | shape | sighash fields after StakeFields |
| --- | --- | --- | --- | --- | --- |
| Delegate {bundle 1, validator 2, stake 4, amount 5, derth 6} | derth/<v> | v_in = derth | – | spend + create | Bytes(validator), amount, derth |
| Restake {bundle 1, validator 2, stake 4} | derth/<v> | – | – | spend + create | Bytes(validator) |
| Undelegate {bundle 1, validator 2, amount 3, stake 5, pc 6, ciphertext 7} | derth/<v> | v_out = amount | – | spend + create | Bytes(validator), amount, pc, Bytes(ciphertext) |
| LockPosition {bundle 1, validator 2, amount 3, splits 4, stake 6} | derth/<v> | v_out = amount | – | spend + create | Bytes(validator), amount, Bytes(SplitsBytes(splits)) |
| UnlockPosition {bundle 1, position_id 2, stake 4} | derth/<position's v> | v_in = position's derth | – | spend + create | position_id |
| UpdatePosition {bundle 1, position_id 2, splits 3, stake 5} | 0 | – | – | nothing | position_id, Bytes(SplitsBytes(splits)) |
| PositionVote {bundle 1, position_id 2, proposal_id 3, options 4, stake 6} | 0 | – | – | nothing | position_id, proposal_id, Bytes(OptionsBytes(options)) |
| Redelegate {bundle 1, src_validator 2, dst_validator 3, amount 4, stake 5, dst_derth 6, move_time 7} | derth/<src> | v_out = amount | derth/<dst>, cr_v_in = dst_derth, cr_move_time = move_time | spend + create, both lanes | Bytes(src), Bytes(dst), amount, dst_derth, move_time |

"spend" = nf_0 **and** nf_1 non-zero: each slot spends a note or pads with
its own would-be nullifier `H(TAG_NF, nk, rho, 0)` for a fresh random rho
(amount 0), so a merge of two notes and a spend of one look the same; a zero
nf_1 beside a non-zero nf_0 is refused (a format change from "nf_1
optional"; wallets always pad slot 1). "create" = cm non-zero. Position msgs prove the position's owner tag. `MsgRestake` merges
an owner's second note at a validator (two devices, a labelled note beside an
unlabelled credit); it never splits.

Encodings: `SplitsBytes` is 16 bytes per entry (option_id, percent, big-endian
u64). `OptionsBytes` is per option the option as big-endian u64, then the
weight's canonical decimal string (`LegacyDec.String`, 18 places) prefixed by
its length as big-endian u32. At most `MaxOptionsPerVote` (4) options.

Responses: `MsgDelegateResponse {derth 1, position 2}`,
`MsgUnlockPositionResponse {position 1}`, `MsgRedelegateResponse {value 1,
derth 2, position 3, completion_time 4}` (`position` the merged note's),
`MsgUndelegateResponse {value 2, payout_id 4}` (1, 3 reserved),
`MsgLockPositionResponse {position_id 1}`.

**Gas** (`PrivateActionGas`, on top of the bundle's): base + proof_verification_gas
+ writes × note_gas, writes = 2 per nullifier slot (an indexed-tree insert
rewrites two paths) + 1 per output slot: lane A 2 nullifiers and 1 output,
lane B (Redelegate) 1 and 1, Undelegate +1 for its queued payout. Bases:
Delegate 400,000; Restake 100,000; Undelegate 400,000; Lock 400,000; Update
300,000; Unlock 300,000; PositionVote 250,000; StakeVote 250,000 (its writes:
1 + 2 vote nullifiers, padding included: the same for every vote); Redelegate 700,000 + 2,500 per entry of the pair's
x/staking record, + 2,500 per entry + 128 × 20,000 while the pair is at the
entry cap (wallets simulate).

Errors: 1101 invalid msg, 1102 validator cannot take delegations (unknown,
jailed, tombstoned, slashed to nothing, or a book settling: no derth but
backing), 1103 amount (below min_delegation, above a note, a credit the value
does not buy, more derth than exists, a vote above the snapshot supply), 1104
unbonding record is not open, 1106 no voting, 1108/1109 position, 1113 stake
tree (anchor, clear_before, debt_root), 1114 stake nullifier spent, 1115 stake
proof, 1119 vote nullifier used, 1120 redelegation (same validator, move_time
out of range, genesis redelegation records).

### 8.4 Delegation and undelegation

**Delegate**: the bundle releases `amount` uerth into the module (queued,
delegated at the epoch end); `derth` credited at the live rate as 8.1.

**Undelegate** names its payout destination; the chain pays it:

- The proof spends derth/<v>, `v_out = amount`, change or a zero note back.
  `pc` is a pool pc (normally the wallet's own) and `ciphertext` its 177-byte
  v2 blind ciphertext, both bound by the sighash and checked like any mint.
  An undelegation worth more than 2^63−1 is refused.
- The chain books the derth's live value u into the epoch's UnbondRecord
  (requested, target, outstanding += u; pending_undelegation; supply −= amount)
  and queues an `UnbondPayout {id, validator, epoch, value = u, pc,
  ciphertext, payout_attempts, retry_at}`.
- The epoch end undelegates each record's target from x/staking; maturity
  reads the SDK entry's balance into record.payout.
- **Payout**, in the EndBlocker after the record matures (never in the
  maturity block: x/staking pays the entry after this module's EndBlocker):
  `u × payout / requested` uerth minted to pc with the ciphertext
  (`MintNoteSplit`); the record settles (outstanding −= u, paid += pay); on its
  last payout the floor's dust goes to the community pool and the record is
  removed. Event `shieldedstaking_unbond_payout {payout_id, validator, epoch,
  value, amount, notes, positions}` after the mint events.
- Bounded, never dropped: at most `UnbondPayoutSweepLimit` (50) payouts tried
  and `UnbondPayoutNoteBudget` (256) notes a block (a payout passing the
  budget waits, unless it is the block's first). Due retries first, then
  untried payouts of matured records, oldest first. A failed payout:
  `payout_attempts` + 1, `retry_at = now + 3600 << min(attempts − 1, 8)`
  (capped at 256 h), moved to the retry queue; event
  `shieldedstaking_unbond_payout_failed {payout_id, validator, epoch,
  attempts, retry_at, error}`.
- Slashing: a slash while the record is PENDING cuts its target
  (BeforeValidatorSlashed); a slash of the SDK entry cuts its balance; every
  payout falls pro rata.
- State: `UnbondPayouts`, `PayoutsByRecord` (untried), `PayoutRetries`,
  `MaturedRecords`, `UnbondPayoutSeq`. `Query/UnbondPayout {id}`
  (`/earth/shieldedstaking/v1/unbond_payouts/{id}`): the payout and its
  record; not found once paid.

The payout's pc and ciphertext link the undelegate tx to its payout notes
(as its public amount would anyway); the notes' later spends are unlinkable.

### 8.5 Stake votes

    MsgStakeVote {bundle 1 (fee), proposal_id 2, validator 3, options 4, weight 5,
                  proof 8, vote_nullifiers 10 (exactly 2), debt_root 11}
                  reserved 6 ("fee"), 7 ("stake"), 9 ("vote_nullifier"); response field 1 reserved
    public inputs: snapshot.root, snapshot.nf_root, debt_root, AssetID(derth/<validator>),
                   weight, proposal_id, vote_nullifiers[0..1], sighash
    sighash fields: proposal_id, Bytes(validator), Bytes(OptionsBytes(options)), weight,
                    vote_nullifiers[0..1], debt_root

- ValidateBasic: exactly 2 vote nullifiers, canonical, non-zero, distinct
  (`CheckVoteNullifiers`; an unused slot carries its padding nullifier);
  weight > 0 with at most three significant decimal
  digits (`RoundVoteWeight`: 399,999,999 -> 399,000,000; `VoteWeightSigFigs`
  = 3), so weights fall in shared buckets; options valid.
- The chain refuses: no open snapshot, a snapshot without nf_root (1106), a
  debt_root that is not current, weight above the validator's snapshot
  supply (1103), any vote nullifier already used on the proposal
  (`UsedVoteNullifiers`; `ErrVoteNullifierUsed` 1119, final, no re-vote).
- It records ONE StakeVote under `0x00 || vote_nullifiers[0]` with both
  `vote_nullifiers` (field 7) and derth = weight, and marks both used on the
  proposal (padding included). Nothing is spent or minted.
  Event `shieldedstaking_stake_vote` carries `vote_nullifiers`
  (comma-separated hex, slot order).
- Tally: weight at the snapshot rate; private votes are fractions of the
  validator's snapshot supply applied to the module's CURRENT shares at the
  validator and deducted from the validator's inherited vote. Total power
  never exceeds bonded stake.

What it allows: one note votes on every open proposal, each with its own
vnf, unlinkable to each other and to its spend without nk (except by the
public weight and validator). A note spent before the snapshot cannot vote;
a note minted after is not under the root; a note spent after the snapshot
still votes, and its outputs cannot. A labelled note votes `amount − exposed
+ retained` under the current debt tree. A top-up after the snapshot keeps
the old value's vote with the old note (wallets keep spent notes' openings
while a proposal snapshotted before their spend is open). One weight per
owner and validator; a wallet staked with several validators sends one vote
each. The number of notes voted (1 or 2) is hidden (padding, 4.2).

### 8.6 Positions (Groundworks)

A position is a public object `{validator, derth, owner tag, splits,
split_epoch}`: `MsgLockPosition` moves derth out of a note into it
(`min_position` 1 ERTH; at most 2^63−1), `MsgUpdatePosition` re-splits it,
`MsgPositionVote` votes it on an x/gov proposal (stake-tree snapshot rule:
created before the snapshot's block; replaceable), `MsgUnlockPosition`
merges its derth back into the owner's note. Each proves the owner tag.
Positions are uncapped. A position keeps its Groundworks weight whatever
its validator's status (jailed, unbonding, unbonded) until its lease ends:
it is the holder's private stake, not the validator's power. Only an
operator's self-bond is gated to Bonded validators (section 9). Decided, not
an oversight.

A position's split is leased: `split_expires_at` = cast or renewed +
`groundworks_lease_seconds` (section 9), 0 without a split. Lock and Update
set it (Update with the same split renews); at the lapse the split is cleared
and comes off its validator's totals at that exact time (section 9,
Leases). Query/Position(s) and the `position` events show it, so a wallet
reminds from its own positions; nothing new ties a position to an owner.

Weighed per validator: x/shieldedstaking keeps `T[v][o]` = Σ over v's live
positions of derth × percent (`GwTotals`, exact integers); Lock, Update and
Unlock add or remove exactly the position's own terms. All of v's positions
are ONE weighted voter in x/allocation, key `"gwpos/" || val_bytes` (26 or 38
bytes, never an account's 20/32 or a nullifier's 32), with weight per option
`trunc(epoch_rate_v × T[v][o] / 100)` (`Voter.option_weights`,
`allocation.SetWeightedVoter`). The epoch end, a later book in the sweep, a
slash and both books of a redelegation (and the source and every
destination of a slashed redelegation) re-file one voter per validator at
the end of the block. A Groundworks stream reset is honoured lazily: stale
`split_epoch`/`GwEpoch` count as zero; the owner re-votes. A split naming an
option pruned since is left out when re-filed and dropped from the export.
A position's weight is not stored; queries compute derth × epoch rate.

### 8.7 Redelegation and the slash debt

**Moving the value** (`redelegate.go`, run in the ante, atomic with the spend):

1. The module's rewards at src and dst are withdrawn into their queues (W ->
   P: neither backing changes).
2. `u = floor(amount × B_src / S_src)`.
3. Pro rata to src's book: `queued = floor(u × P_A / (D_A − U_A + P_A))` out
   of the queue as a book entry, `bonded = u − queued` (never above D_A −
   U_A). Queue first only where no slash can reach src's stake: src is
   Unbonded, or `D_A − U_A ≤ 0`. A bonded part of at most `bondedDust` (1,000
   uerth) stays with src's book (the mover's loss).
4. The bonded part moves with x/staking's primitives (`moveBonded`: Unbond at
   src, Delegate at dst, token source = src's status) and the module records
   the redelegation entry itself: one entry per (src, dst, block height),
   moves in one block share it. No transitive rule and no max_entries apply.
   At `MaxEntryHeightsPerPair` (1,024) entries of positive height, a move
   first merges the two oldest adjacent entries whose moves (at most
   `MaxMergeMoves` = 128) can be re-filed, trying at most `MergeTries` = 8
   pairs: the merged entry takes the later height and the earlier
   completion, the merged-away entry's unbonding id is deleted, its moves'
   entry height and completion follow; then the move adds its own entry. A
   move is never in an entry older than itself; joining the latest entry is a
   last resort when no pair qualifies. Src unbonded: no entry; src unbonding:
   the validator's unbonding time and height. Entries at height ≤ 0 are not
   counted and never merge.
5. dst credits `dst_derth` (`creditDst`: ≤ what arrived buys at dst's rate,
   B and S before the move; ≥ min_delegation); supply checkpoints for open
   snapshots are written first.
6. The proof's lanes spend the src notes and the dst note (or padding), and
   create the change and the merged, **labelled** dst note.

`move_time` must be within `[block time − MoveTimeSlackSeconds (600), block
time]`. Refusals: same validator (1120), dst not delegatable (1102), more
derth than exists, value or credit below min_delegation or above a note, a
credit the value does not buy (1103), move_time out of range (1120).

A move with an entry is recorded (`Move {key, src, dst, height, move_time,
credited, shares, entry_height, completion, retained}`, key = the credit
nullifier) until the entry matures; indexed by entry and by completion.

Event `shieldedstaking_redelegate {src_validator, dst_validator, derth,
value, credited, queued, bonded, completion_time (unix ns; "" when no
entry), move_key, move_time}`, then the stake nullifier and stake note
events.

**Labels.** A note holds at most one label; the exposure never leaves its
note while the label is open: lane A spends at most one labelled note and
keeps the exposure in the output; lane B merges only into an unlabelled note
(a redelegation into a validator where the owner's note is labelled makes a
second note). Undelegating, locking or redelegating exposed derth waits for
the label to clear; the unexposed part moves freely. Chained moves (X -> A
-> B within the window) therefore label each move separately.

**Window.** A label may clear once `move_time + window < clear_before ≤ block
time`, window = the longest x/staking unbonding_time ever seen
(`MaxUnbonding`; a governance cut does not shorten old windows) + 600 s. By
then the entry has matured and x/staking no longer slashes it.

**Attribution.** As a slash begins (x/staking's BeforeValidatorModified on
src, outside txs) the module opens a watch (its shares at each destination of
its src redelegations), then counts x/staking's Unbond of its delegation at
each dst (BeforeDelegationSharesModified: one per slashed entry). It settles
at the next slash or at its BeginBlocker (ordered right after x/slashing and
x/evidence, before any tx). Per dst, the slashed entries are found by
replaying x/staking's SlashRedelegation on the pair's entries for each
fraction x/staking is ever called with (x/slashing's slash_fraction_downtime
and slash_fraction_double_sign, read in the same block) and each infraction
height an entry boundary allows: an entry is slashed iff at or above the
height, unmatured, `trunc(f × initial_balance)` and `f × shares_dst` both
non-zero, and the delegation still there; it takes min(f × shares_dst, the
delegation's shares). The replay that makes exactly the counted unbonds and
burns exactly the fall of the module's shares at dst is the slash. Then

    value = TokensFromShares(burnt) at dst
    debt  = floor(value × S / (B + value))          (B, S after the burn)

comes off dst's derth_supply (`ValidatorState.slash_debt` += debt): dst's
rate is unchanged for every honest holder. Each move of a slashed entry owes
debt pro rata to its share of what the slash took from its entry: retained −=
its part, and its debt row is written. With no matching replay, an event is
emitted and dst's book absorbs it. Events `shieldedstaking_slash_debt
{src_validator, dst_validator, value, debt, entries}`,
`shieldedstaking_move_slashed {move_key, src_validator, dst_validator, debt,
retained}`, `shieldedstaking_debt_row {move_key, retained, index, root}`.

While a slash of src runs, the module's unbonding delegation at each
destination is set aside (`ShelteredUnbondings`) and restored at its
BeginBlocker and EndBlocker, so dst's undelegations already under way are
untouched; rewards at the destination are withdrawn into the queue first.

**Solvency.** derth_supply counts unlabelled derth at face value and
labelled exposures at what they are still worth. Exposed notes stay at dst
until they clear, so every unit of debt is owed by a note in dst's book; a
note pays when it clears (`amount − exposed + retained`) and can clear only
after its entry matured. Invariant 4 holds throughout.

**Zero-height export.** x/staking's prep moves every entry to height 0;
`ResetHeightsForZeroHeight` drops the open moves and keeps the debt rows: a
label clears against the debt tree alone (its row's retained, or whole when
never slashed).

Queries: `Query/DebtTree {start, limit}` -> rows (insertion order), size,
root, window_seconds, clear_before (`/earth/shieldedstaking/v1/debt_tree`);
`Query/Move {key}` -> the move while open, slashed, retained
(`/earth/shieldedstaking/v1/moves/{key}`).

### 8.8 Validator income (reward escrow)

Each validator has a reward escrow, `RewardEscrowAddress(val) =
address.Module("shieldedstaking", "reward_escrow", val)` (32 bytes, no key,
not blocked), and the chain sets the operator's x/distribution withdraw
address to it (AfterValidatorCreated, before MsgCreateValidator's
self-delegation; InitGenesis for every validator; distribution's store
setter). Everything distribution pays the operator (withdrawals, self-bond
rewards paid on self-bond changes, commission, the force-withdraw at
removal) lands in the escrow. The bank send restriction seals it: coins in
only from x/distribution, out only to its operator (`RewardEscrows` maps
escrow -> validator).

At each epoch end every active (bonded, unjailed) validator's self-bond
rewards and commission are withdrawn to the escrow, and its uerth balance
moves to the operator and is self-delegated in the same guarded cache
context: never liquid. Jailed or unbonded validators skip. When x/staking
removes a validator, the escrow is released (every denom) to the operator
(`RetiringEscrows`, at most `EscrowRetireLimit` 50 a block; a failed release
retried after `EscrowRetryDelay` 24 h via `PendingReleases`), forgotten, and
the withdraw address reset. Refused everywhere (ante, authz MsgExec,
`app/operator_router.go` for gov, group, ICA host and contracts): operator
MsgWithdrawDelegatorReward, every MsgWithdrawValidatorCommission, and
MsgSetWithdrawAddress (1116/1117). Compounding resets an operator withdraw
address found pointing elsewhere. A vesting account cannot operate a
validator (1118). The only way to take income out is to unbond the self-bond.

---

## 9. Allocation (Groundworks)

- The validators' Groundworks voters are x/shieldedstaking's weighted voters
  (8.6).
- A self-bond counts toward Groundworks weight only at a Bonded validator
  (`bondedWeight`, `PositionWeightSource.Weight`); the operator is resynced
  when its validator bonds or starts unbonding, and while its bond remains
  its vote is kept at weight zero (the weight returns with no new vote). A
  self-bond withdrawn takes its weight with it.
- A voter's split drops options pruned since it was cast (at its next resync
  and in the export).
- **Leases.** Every Groundworks split is leased for
  `groundworks_lease_seconds` (x/allocation param 2; 0 = default 365 days;
  else 1 day to 2 years), as a caretaker split is for caretaker_vote_seconds:
  a stake position's (`Position.split_expires_at`, 8.6) and an operator's
  (`MsgSetAllocations`, `Voter.expires_at` field 5) alike. Operators get the
  same rule because their vote is the same kind of power (stake directing
  public money); one param keeps the stream uniform. Casting again renews
  the lease (from that block); a resync at a new weight keeps it. Genesis
  refuses an account's Groundworks split with no `expires_at`, as it refuses
  a position's split with no `split_expires_at`.
  - **Where a lease retires: only x/allocation's BeginBlock sweep**
    (`SweepLapses`, from the BeginBlocker's settle of each stream). It walks
    every lease due by the block time in time order (`VoterLapses` here,
    x/shieldedstaking's `GwLapses` through the registered `Lapser`), settles
    the index to each lapse time and retires that weight there
    (`SetWeightedVoterSettled` / `writeVoter`, no further settle), then
    settles to the block time: the emission after a lapse is never shared
    with the lapsed weight. It drains the whole due queue, uncapped, so
    after it nothing is due for the rest of the block. After a halt of H
    seconds that is the retirements of the splits cast in an H-long window a
    lease earlier, in one (slower) block.
    **Bound** (audit R3-C1): a lease ends at cast block time + L, so one
    block's casts share one lapse second and only a halt packs several
    blocks' worth into one sweep. Position casts (Lock / Update) are private
    actions, at most `max_private_actions_per_block` per block (32 by
    default, 256 at most); account leases are operators' only, one per
    operator. The first block after a halt of H seconds therefore retires at
    most `max_private_actions_per_block` x H / block_time positions (32 per
    block of halt: a 6 h halt at 6 s blocks, about 115k, each some 10-20 KV
    operations and one event), plus at most one lease per operator. The
    total work is what those blocks would have done; only its spreading is
    lost. It cannot be weaponised: reaching the bound needs every block of
    the matching window, one lease (365 days) before the halt, full of
    paid position casts, which crowds out every other private action for
    that window and, since a position holds one lease, means about
    32 x L / block_time (~1.7e8) live positions renewed round-robin, all to
    hit a halt no one can schedule a year ahead. The worst case is one
    slow, event-heavy block, not a halt.
  - **Every other settle** (a tx, a staking hook, an EndBlock resync:
    `AdvanceIndex` / `advanceIndexTo`) moves the index only and never
    touches a voter, position or total, so a caller that reads a position
    or voter, settles, then writes cannot undo or double a lapse. Callers
    also settle before they read. Should a lease ever be due at such a
    settle (only a module settling Groundworks in BeginBlock before
    x/allocation could cause it; none does), it stops the index at that
    lapse time instead of passing it. (Audit round 2, CD-1: before, any
    settle retired leases, capped at 1000 lapse seconds; a backlog past the
    cap left leases due at tx time, where read-settle-write callers
    resurrected lapsed weight, subtracted a position twice so its option's
    last position could never unlock, or re-filed a lapsed operator vote
    with no lease.)
  - A lease whose retirement fails is re-queued a day after the block it
    failed in (`LapseRetryAt`, `LapseRetrySeconds`), counting meanwhile,
    never halting. A deterministic failure therefore keeps its weight, and
    its options keep earning on it, for good: alert on it.
  - Events: `split_lapsed` (operators), `position` with action
    `split_lapsed` (positions; every position event carries
    `split_expires_at`).
  - **Alerting** (x/allocation/types/events.go; none halts anything):

    | event | attributes | meaning | alert |
    | --- | --- | --- | --- |
    | `lease_retire_failed` | `stream`, `lapser` (`account` / `positions`), `key` (voter / valoper), `expires_at`, `retry_at`, `error` | a due lease could not be retired; it counts until `retry_at` and retries daily for ever | any occurrence (page) |
    | `lease_settle_held` | `stream`, `expires_at` | a settle other than the sweep met a due lease and held the index there; should never happen | any occurrence (page) |
    | `lease_backlog_drained` | `stream`, `lapse_seconds` | one sweep walked more than 1000 lapse seconds (a halt's backlog), exact but slow | informational |

    x/shieldedstaking also emits `shieldedstaking_epoch_failure` (`stage`
    `lapse_positions`) alongside `lease_retire_failed`.
- The gov module account is on the blocked list.

---

## 10. Dex

Private msgs (fee from the bundle's uerth balance; actions run in the ante,
atomically with the spend):

| Msg | releases | sighash fields | gas |
| --- | --- | --- | --- |
| MsgNoteSwap {…, denom_in 8, amount_in 9} | denom_in | Bytes(denom_in), amount_in, Bytes(denom_out), min_amount_out, pc, Bytes(ciphertext) | 300,000 + 1 note |
| MsgAddLiquidityShielded {…, erth_amount 11} | both legs | pool_id, Bytes(min_shares), share_pc, Bytes(share_ciphertext), refund_pc, Bytes(refund_ciphertext), erth_amount | 300,000 + 3 notes |
| MsgRemoveLiquidityShielded | dexlp/<pool> | pool_id, erth_pc, Bytes(erth_ciphertext), token_pc, Bytes(token_ciphertext) | 200,000 + 2 notes |

- Swap: the asset in goes to the module (`ReleaseToModule`) and into the
  reserves; the output is minted as a note to `pc`. The fee is the whole uerth
  balance unless denom_in is uerth (then balance − amount_in). A holder of
  only ANML needs an ERTH note to pay a swap's fee. The swap fee rounds up.
- LP shares are private: `dexlp/<pool>` is a pool-locked pool asset;
  `MsgAddLiquidityShielded` mints the shares as a note to `share_pc` and what
  the ratio does not take back as refund notes (one ciphertext for both refund
  notes). Only pool reserves and module-held liquidity are public.
- **LP share notes are transferable, not owner-locked.** A `dexlp/<pool>`
  note is an ordinary pool note: any bundle may spend it and output it to any
  pc, so a shielded transfer (MsgSend with only the fee as balance, no
  receiver) hands shares to another person privately, and the new owner may
  withdraw them. "Pool-locked" refuses only an unshield (`checkUnshield`:
  `IsPoolLocked`, before anything is spent; x/shielded 1109 `ErrSendRestricted`): shares
  leave the pool only through `MsgRemoveLiquidityShielded`
  (`ReleaseToModule`). Owner-locked notes are stake notes alone (3.3).
  Test: `TestDexAnmlPoolLiquidity` (the transfer passes every rule, the
  unshield is refused).
- `MsgRemoveLiquidityShielded` releases exactly `dexlp/<pool>` into an
  `LpUnbonding` with no address (`withdrawal_id = 0x00 || first nf`); at
  maturity both legs are minted as notes (`MintNoteSplit`). A withdrawal whose
  note leg is above `32 × (2^63 − 1)` (a quarter of the split capacity) is
  refused when it starts (1101). A payout that fails keeps its escrowed
  shares: `payout_attempts` (LpUnbonding field 10) + 1, completion moves to
  `now + 3600 << min(attempts − 1, 8)`; event `lp_unbond_payout_failed`
  carries `attempts` and `retry_at`. At most `LpUnbondSweepLimit` (50)
  payouts and `LpUnbondNoteBudget` (256) notes a block (a payout passing it
  waits unless it is the sweep's first).
- Signed paths minting notes: `MsgBuyAnml` (ANML minted as a note),
  `MsgRemoveLiquidity` of a shielded-only pool (its ANML leg minted to a
  stored pc). ANML exists only as notes: every transparent ANML leg is
  refused.
- Every other private mint is atomic with its msg (an oversized value fails
  the tx) or bounded far below 2^63.
- `volume_depth_cap_per_day` is at most 1,000.

---

## 11. Genesis, invariants, exports

### 11.1 x/shielded

Genesis fields: params 1, assets 2, commitments 3 (the note tree, in
position order), nullifiers 4, roots 5, turnstiles 6. InitGenesis rebuilds
the tree and checks the recorded roots against it; the turnstile invariant
(5.3) checks every turnstile against the bank.

### 11.2 x/shieldedstaking

Genesis fields: params 1, epoch 2, validators 3, unbond_records 4, positions
5, next_position_id 6, snapshots 7, votes 8, stake_commitments 9,
stake_nullifiers 10 (insertion order), stake_roots 11, snapshot_seq 12,
supply_checkpoints 13, epoch_sweep 14, pending_releases 15, retiring_escrows
16, unbond_payouts 17, next_unbond_payout_id 18, moves 19, debt_rows 20
(insertion order, latest retained), max_unbonding_seconds 21.
`ValidatorState.slash_debt` is field 9.

InitGenesis: re-inserts stake nullifiers in order and checks every
snapshot's nf_root against the tree at its nf_size; rebuilds
`UsedVoteNullifiers`, Groundworks totals, the debt tree and reward escrows
(not exported; refuses an operator whose withdraw address is neither itself
nor its escrow); loads the moves before checking x/staking's redelegations:
every redelegation must be the module's (a genesis redelegation by anyone
else is refused), every unmatured module entry's shares must equal its moves'
shares exactly, at most 1,024 entries of positive height per pair; entries at
height ≤ 0 carry no shares and no duplicates (a move there must still match
such an entry's completion).

Validate refuses: a zero or repeated stake nullifier; a malformed nf_root or
an nf_size beyond the tree; a repeated or malformed vote key; a note vote
without exactly 2 non-zero vote nullifiers (the first its key's) or sharing one with
another vote on the proposal; a position vote with any; payouts per record
not summing to its outstanding, values outside 1..2^63−1, a payout without a
pc or blind ciphertext, retry_at not set exactly when attempts > 0; a
genesis position's derth outside 1..2^63−1; move and debt row keys not
canonical, distinct and nonzero; retained outside 0..credited; a cut move
without its row; a move or debt row key that is not a spent stake nullifier;
a move's completion different from its entry's. x/personhood genesis refuses
records dated after genesis and handle leases past genesis +
handle_lease_max.

Invariants (`AssertInvariants`; the epoch end reports, never halts; skipped
with an event above `InvariantBookLimit` 1,000 books/records/positions; each
redelegation record decoded once a pass):

1. ERTH: module uerth balance == queued delegations + matured payouts not yet
   made.
2. derth is never a coin; derth locked in v's positions ≤ derth_supply_v.
3. Unbonding: per validator, UNBONDING records' sum == the module's SDK
   entries' initial balances; each record's height has an entry.
4. Rate: pending_undelegation == PENDING targets, `D + W + P ≥ U`, and
   `derth_supply × rate == D + W + P − U` (short by at most
   ceil(supply × 1e-18) + 1 uerth, never over).
5. Reward escrows: every validator's escrow recorded and its withdraw
   address; no other; the module account's withdraw address is itself.
6. Groundworks totals == Σ derth × percent of live positions.
7. Stake nullifier tree (O(1)): size 0 or 1 + its last value's leaf index;
   recorded latest size ≤ size.
8. Payouts: each record's payouts sum to its outstanding; every payout in
   exactly one queue; every MATURED record with untried payouts marked.
9. Redelegations: every x/staking redelegation is the module's, between two
   different validators, 1..1,024 entries of positive height in
   creation-height order, every unmatured positive-height entry holding
   exactly its moves' shares; no unbonding delegation left set aside.
10. Slash debt: every unmatured move is in an unmatured entry of its pair at
    its height with its completion; retained is its row's (or its credit);
    the debt tree's size is 0 or 1 + its last leaf index, one key and one
    retained per leaf; no slash left watched outside BeginBlock.

### 11.3 Exports

Allocation and position splits drop options pruned since. A zero-height
export drops open moves (8.7).

### 11.4 Launch ceremony

The committed genesis is the placeholder until the operator runs
`scripts/ceremony.sh --launch <launch.json> --genesis-time <RFC3339> --memo-peer ID@HOST:PORT --moniker NAME`
(networks/genesis/README.md): the operator, its consensus key, the
placeholder accounts to remove and the keys never to reuse come from the
operator's launch file (its mnemonic is read at run time only), and the
genesis is rebuilt. The genesis sha256
at the top of this document is the placeholder's; the ceremony prints the
launch one. `TestLaunchCeremony` reports PENDING CEREMONY until then
(`EARTH_REQUIRE_CEREMONY=1` fails instead).

The `--memo-peer` host is the launch genesis's only advertised peer, so it
must be dialable from anywhere: a global IP (private, CGNAT 100.64.0.0/10,
loopback, link-local, reserved, documentation and multicast ranges are
refused, IPv4-mapped ones too), or a fully qualified DNS name that **must
resolve to public addresses only**. The script resolves a name and refuses
it if it does not resolve or any address is not global, and refuses
internal suffixes (`.local`, `.internal`, `.lan`, `.home.arpa`, ...). It
cannot see split-horizon DNS: use a name only if it resolves the same,
publicly, everywhere; prefer the public IP. (Audit round 2, CD-4.)

---

## 12. Wallet formats

### 12.1 Ciphertexts

    v1 note (every bundle output, 217 bytes):
      ct  = epk (32) || ChaCha20-Poly1305(key, nonce = 12 zero bytes, pt)
      key = HKDF-SHA256(X25519(esk, ek_pub), salt "earth.note.v1", info epk || cm)
      pt  = 0x01 || asset_id (32) || value (u64 BE) || rho (32) || rcm (32) || memo (64)

    v2 amount-blind note (every chain mint to a hidden owner, 177 bytes):
      salt "earth.note.v2", info epk
      pt  = 0x02 || rho (32) || rcm (32) || memo (64)

    wallet stake note (every stake proof output, 201 bytes):
      ct  = epk (32) || AEAD(0x04 || asset_id (32) || amount (u64) || rho (32) || rcm (32) ||
                             move_key (32) || move_time (u64) || exposed (u64)) || tag (16)

A v2 ciphertext is bound to its note by the cm check: the owner opens it,
recomputes pc and cm from the published denom and amount, and accepts only a
matching cm. The chain checks only the stake ciphertext's length (the wallet
defines its AEAD). Wallets keep no self-mint counters: every owned note is
found by trial decryption and the cm check, or (referral notes) by owner_pk.
Ciphertexts in msgs are at most `MaxCiphertextBytes` (1,024).

### 12.2 Building txs

- **Gas and memo** are chosen before proving (simulate: proofs are not
  verified there); gas_limit = simulated + 10%.
- **Bundles**: at least 2 actions, one anchor; pad to a fixed bucket where
  possible (action count leaks shape).
- **Stake proofs**: clear_before = `Query/DebtTree.clear_before` and
  debt_root = `Query/DebtTree.root`, read at proving time (accepted while
  clear_before ≥ the chain's − 3,600 s and the root is current; a slash
  reaching a redelegation changes the root: re-prove). Owner tag salt as
  8.2.
- **Validator list** (`Query/Validators`, `/earth/shieldedstaking/v1/validators`):
  wallets read **every page** of it, at one height (the response's `height`;
  pin later pages with the `x-cosmos-block-height` header), and quote from
  it; never ask about one validator (`Query/Validator`, x/staking's
  per-validator queries) before a staking msg: that ties the asking IP to
  the intent. Pages walk x/staking's validators in its key order (standard
  `PageRequest`; limit at most 200, default 100; `count_total` on an offset
  page); the last page also carries the books of validators x/staking has
  removed (`staking.operator_address` ""). Each `ValidatorQuote`: x/staking's
  `Validator` (status, jailed, tokens, shares, commission, description,
  unbonding time), `tombstoned`, `delegatable` and `refusal` (why a
  delegation or a redelegation into it would be refused: unknown, jailed,
  tombstoned, slashed to nothing, or settling), `book` (P =
  pending_delegation, U = pending_undelegation, S = derth_supply,
  epoch_rate, slash_debt), `backing` B, `supply` S, `rate`, `delegation` D,
  `rewards` W, and `redelegations` (the module's x/staking entries out of it
  per destination: `entries`, `counted_entries`, for MsgRedelegate's gas,
  8.3). Snapshots (votes) and the debt tree are global queries already.
- **Delegate quote**: B and S from the list; derth = floor(amount ×
  S / B) less a margin for the rate's drift until the tx lands (~10 ppm), or
  amount exactly while S = 0. Lane A spends your derth/<v> note or pads (a
  fresh rho, position 0, nf = H(TAG_SNF, nk, rho, 0)); output = old + derth (a
  labelled input keeps its label and exposure).
- **Undelegate / Lock**: the change, or a zero note (amount 0, fresh rho/rcm)
  when nothing is left; a labelled note releases only its unexposed part (or
  clears first). Undelegate: a fresh pool opening, pc = PC(owner_pk, rho,
  rcm), ciphertext = EncryptBlindNote(rho, rcm, memo) to your own key;
  remember payout_id; find the payout by trial decryption of `shielded_mint`
  (several notes may share one ciphertext) or by
  `shieldedstaking_unbond_payout.payout_id`.
- **Redelegate quote**: value = amount × rate_src; dst_derth =
  floor(arrives × S_dst / B_dst) less both rates' margins, arrives = value −
  1,001 uerth (a bonded part up to 0.001 ERTH stays with src and x/staking
  truncates a uerth), or value exactly when src is Unbonded and its queue
  covers it. The split (8.7 step 3) uses src's `delegation` D, `book` U and
  queue P + `rewards` W (the rewards join the queue first); dst must be
  `delegatable`. Lane A as an undelegation; lane B spends your unlabelled
  derth/<dst> note (or pads), cr_v_in = dst_derth, cr_move_time = move_time =
  the latest block's time. Gas varies with the pair's entry count: simulate.
  A refused quote costs nothing.
- **Unlock**: lane A merges the position's derth into your note there.
- **Clearing**: after move_time + window, any lane-A proof may clear, with
  the witness from the debt rows (rebuild with zk/debt from Query/DebtTree or
  `shieldedstaking_debt_row` events; a move without a row: its low leaf).
- **Votes**: `Query/Snapshot(proposal_id)` for root, tree_size, nf_root,
  nf_size. Up to two of the owner's derth/<validator> notes under the
  snapshot (position < tree_size, nullifier absent under nf_root, vnf not
  used). Rebuild the nullifier tree at the snapshot from the first
  nf_size − 1 stake nullifiers in insertion order (`Query/StakeNullifierTree
  {start, limit}`: values from leaf start+1, up to 1,000 a page, with size
  and roots; or `shieldedstaking_stake_nullifier` events, which carry
  `index`; read failed txs too); its root must equal nf_root. Low leaf of nf:
  the predecessor (else the sentinel: value 0, index 0) with next = nf's
  successor (else 0, 0). vnf = H(TAG_VNF, nk, rho, pos, proposal_id); weight
  = RoundVoteWeight(Σ values); an unused slot carries padding (below); debt_root the current root. Prover
  arrays per slot (amount, rho, rcm, pos, path, low_*, label and debt
  witness, vnf). See the padding rule below.
- **Vote padding (wallet rule).** A vote always carries exactly two
  non-zero, distinct vote nullifiers. A vote of one note fills the other
  slot with padding: draw r, a fresh uniformly random field element (a CSPRNG,
  e.g. 64 random bytes reduced mod p) for every vote and never reuse it;
  vnf_pad = H(TAG_VPAD, nk, r, proposal_id) (`privacy.VotePadNF`,
  privacy_core `vote_pad_nf`); put the note and the padding in a random slot
  order. The prover's padding slot: amount 0, rho = r, every other field 0,
  vnf = vnf_pad. vote_nullifiers = the two slots' vnfs in that order (the
  record's key is the first, whichever it is). Remember the notes' vnfs as
  voted; the padding needs no record. Gas: 1 + 2 note writes for every vote.
- **Discovery**: every stake note is a proof output with a wallet
  ciphertext. Find derth credited by a delegation or redelegation in your own
  output (the response's `position` names the merged note).
- **Indexer**: serve stake nullifiers in insertion order with their leaf
  index (page by index) or proxy `Query/StakeNullifierTree`; include failed
  txs' ante events.

---

## 13. Known limitations and audit notes

Audit scope beyond the circuits: bb's MSM black box (`cycle_group` batch_mul)
must be sound for witness points, including `G_s == G_o` and `v = 0`;
gnark-crypto grumpkin is "partially audited" and non-constant-time (the chain
only verifies; wallets sign with their own code); the binding Schnorr is
textbook. The −G canonicality check (2.6.4) is the only defence against that
inflation.

Accepted:

- Action count shows a bundle's shape; wallets pad.
- Fee amounts are wallet-chosen (wallets should round to a fixed schedule).
- Timing between a move and a switch, or a registration and a claim, can link
  a passport to a handle. Wallets suggest a random delay before a move and
  let the user choose when (6.6); they never move automatically.
- Selling the passport: a holder who gives a buyer her passport data (DG1,
  SOD) and an old identity secret lets him register her passport to his own
  identity and move her split or handle to it for one lease (6.2); a
  register proof run as a multi-party computation (her passport, his
  id_secret) does the same without showing him the data. Registration
  proves knowledge of the identity's secret, not who holds the passport;
  nothing on chain can tell. Without the passport data no sale is
  enforceable (audit R2-B1).
- Private stake votes keep their expedited-round vote after a demotion while
  human votes rescope.
- derth is owner-locked, but nk can be sold off-chain. If whole-account sales
  appear, stake notes could additionally be bound to a personhood identity.
- Transparent tx replay across earth-1 relaunches (account numbers are not
  offset).
- A single YES carries an uncontested removal ballot (by design).
- Contract, ICA and group operators; dust undelegations; TWAP precision; the
  export-to-relaunch gap credited at the pre-export price.
- An expired registration proves until the expiry sweep reaches it (6.1).
- A slash with no matching replay falls on dst's book (event emitted).

Tx result bytes (`app/result_cap.go`, audit R6-E-1). A tx's ABCI result
(events, msg responses, log) is stored twice and kept for ever, and every
block_results / tx answer rebuilds it in memory at ~6x. wasmd charges ~1 gas
per byte of contract events and nothing per byte of response data, so before
this one 100M-gas tx could store ~100 MB. Consensus now meters the msg phase
of every tx, at the one point every msg passes (each route of baseapp's msg
router, wrapped in place; nested msgs count once, in the outer msg):

| | |
|---|---|
| free per tx | 8 KiB (1.5x MsgRegister, the largest chain flow at 5.4 KB) |
| past that | 20 gas per byte (twice the tx-bytes price) |
| cap per tx | 1 MiB of msg results in total, and 1 MiB per top-level msg: over it the tx fails with `ErrTxTooLarge` (sdk code 21), msgs reverted, fee charged, stored result < 4 KiB (audit R8-D-1) |
| relay txs | a tx of relay msgs only (packet receive, ack, timeout, v1 and v2; client update and misbehaviour) has no byte cap: gas bounds it (5 MB at max_gas 100M), and no packet is undeliverable for its size (audit R7-C-1, R8-C-1) |
| IBC application | every application route (ICS-20 with callbacks, ICA host, contract ports; v1 and v2) may emit 512 KiB per received packet (events plus twice the ack); past that its state and events are dropped and the packet gets an error acknowledgement. A contract port's ack/timeout past it fails (audit R8-C-1) |
| IBC callback | a callback contract's events capped at 256 KiB: over it the callback fails (on receive: error acknowledgement, the packet is still received) |
| gov proposals (EndBlock) | no gas there; every proposal msg of one EndBlock shares a 1 MiB total, past it the msg errors and its proposal is FAILED (audit R7-C-2) |
| error text | cut to 1 KiB, code and codespace kept: a failed tx's log, nested msgs' errors, IBC callbacks' `callback_error`, the ICA host's and contract ports' error attributes (audit R8-C-1) |
| how bytes count | at their worst-case JSON size, as RPC and LCD answers write them: each event string byte 1, or 2 for `"` `\` and `\n`-style escapes, or 6 for `<` `>` `&`, other control characters and invalid UTF-8 (U+2028/2029: 6 for 3); msg responses 2 per byte (hex in the LCD). Escape-free ASCII counts its length, so the chain's flows cost the same (R8-D-1 follow-up) |

A block then holds at most ~10 MB of results (5 MB paid at max_gas 100M, or
~1,100 contract txs at the free tier). The ante phase is not metered: a signed
tx's ante events are under 1 KB, a private tx's (its notes and nullifiers,
6.5 KB at most) are fixed by its shape and capped per block by
`max_private_actions_per_block`. An ICS-20 MsgRecvPacket is 3.7 KB, 168 KB
with ibc-go's 32 KiB memo maximum, 823 KB when that memo is all '<' (the
packet's JSON escapes it, and core logs the packet hex-encoded twice); a
relay batch past 8 KiB pays per byte (gas is simulated). Relay txs have no
byte cap so that no packet, and no batch holding one, can be failed by its
size: 38 packets with eight max-memo ones store 1.44 MB for 34M gas and are
delivered (`TestResultCapRelayBatch`), and a packet whose callback fails
with a ~0.9 MB error is delivered with a 1 KiB error in an error ack
(`TestResultCapUndeliverablePacket`). What a packet's contents can add is
bounded at the application instead (512 KiB, errors 1 KiB). The free tier
stays per tx, so a tx of many cheap msgs gets one, and the block bound
holds. Since bytes count at their JSON size, the limits bound the answers
too, up to the uncounted JSON structure (at most 3.3x, for attributes with
a one-byte key and an empty value). Worst cases (`app/resultcap`,
`TestResultCapWorstCase`): any non-relay tx ≤ 1 MiB counted, 3.43 MB of RPC
JSON at most; a relay tx 5 MB counted at 100M gas, ≤ ~16.4 MB of JSON;
block_results of a max block ~18 MB of JSON, plus gov's EndBlock. Relayers: keep batches to ~10 msgs and
the gas multiplier at 1.3 or more (simulate prices the bytes). wasmd's own gas register
is unchanged: the meter prices what is stored, for every module, including
response data and IBC acks that wasmd's event costs never see.
