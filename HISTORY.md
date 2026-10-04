# Design history of the privacy branch (`privacy/orchard`)

How the privacy design reached its current form
([ORCHARD_DESIGN.md](ORCHARD_DESIGN.md)), with the alternatives that were
weighed and why they lost. Audit rounds, findings and fixes are in
[AUDIT_HISTORY.md](AUDIT_HISTORY.md); operator-facing changes in
[CHANGELOG.md](CHANGELOG.md). Everything here happened before the privacy
relaunch: every change is a fresh genesis, never a migration.

## Before: the fixed transfer circuit

The first privacy layer used one `transfer` circuit, 3 inputs and 3 outputs
(12,245 gates, 2^14; 0.53 s single-thread prove), with per-module signals
(`SpendSignal`, `ActionSignal`, `MultiSpendSignal`). It could not spend more
than three notes and gave every tx the same fixed shape.

## 2026-10-02: Orchard-style spike (commit 466d952)

Replace the transfer circuit with N per-action proofs, a public value balance
per asset and a binding signature. Decisions:

| Question | Decision | Why |
| --- | --- | --- |
| Assets per action | Spend and output may differ | 187 gates over single-asset (7,933 -> 8,120, same 2^13). A bundle needs max(#spends, #outputs) actions instead of Σ_assets max(...). |
| Value base | In-circuit hash-to-curve with canonical y | A generator registry tree (depth 16) measured 10,552 gates (2^14, ~1.6x prove time) and added a public root, state and a window. Hash-to-curve is stateless and cheaper. |
| Counter minimality | Not enforced | A non-least ctr gives a base that only balances against itself. |
| Binding signature | Schnorr over Grumpkin, base R, Poseidon2 challenge | Same curve as the circuit; no new hash for clients. |
| Sighash in proof | Yes | One public input; proofs cannot be lifted into another tx even if rcvs leak. |
| Public balance sign | Positive only | Value enters only by shield or chain mint; the release accounting stays a plain map. |

Measured (Apple M2, bb v5.0.0, nargo 1.0.0-beta.22): action 8,120 gates; 2
actions in one proof 13,385 (2^14); action without cv 6,000. Prove 0.34 s
(1 thread) / 0.16 s (8); Go verify 4.35 ms per action, 0.12 ms binding check;
10 actions verify in 41 ms serially, 12 ms with a goroutine per action.
Typical txs: fee-only 1 -> 2 actions, ANML send + ERTH fee 3 actions, a
5-note spend became possible. Options noted and not taken: an `action2`
circuit proving two actions per proof.

## 2026-10-02: Phase 1, bundles in production (1c4802a, d015f17)

Differences from the spike: **one anchor checked per action** (dummies
included) with the digest carrying each action's anchor; K (bundle count) in
the sighash; `MsgSend{bundle, receiver, fee}` with the release map paid in the
ante; bases derived, not stored; balances at most 2 per action; 2..32 actions
(`MinActionsPerBundle = 2` for padding); `bundle_gas` + per-action gas;
`max_private_actions_per_block` counting actions; every binding signature
before any proof, proofs in parallel; authorization keyed by the msg's
SHA-256. A `PrivateAnchorAcceptor` hook for out-of-window stake-vote anchors
had no implementer and was removed. The transfer circuit, key and fixtures
were deleted.

The spike had one anchor per bundle (per-action anchors were rejected then
because a two-bundle stake vote needed a snapshot anchor). Phase 1 gave each
action its own anchor field; audit 6 then required every action of a bundle
to name the same anchor (dummies with a different anchor would show which
actions are real), which is the rule today.

## 2026-10-02: Phase 2, every module on bundles (d083cc5)

Personhood and assembly msgs carry a fee bundle; membership proofs bind the
sighash. Dex: private LP shares (`dexlp/<pool>` pool-locked notes; withdrawals
escrowed in an address-less LpUnbonding). Staking: owner-locked stake notes in
x/shieldedstaking's own tree, with a 2-in/2-out stake circuit (9,647 gates)
and chain-minted stake notes (`spc_mint`). Groundworks positions weighed per
validator (one voter per validator, uncapped positions). Validator income
compounds through a sealed reward escrow.

Noted for later: binding stake notes to a personhood identity if account
selling appears.

## 2026-10-02: one fee rule, one note-discovery rule (b330b33, fb3443b)

From the x/shielded audit: the sighash binds memo, timeout_height and
gas_limit; proofs exactly 14,656 bytes; CheckTx verifies proofs sequentially.
User decisions: every chain-minted note carries a msg-supplied amount-blind
ciphertext (wallets dropped self-mint counters); every private msg pays its
fee from its bundles' uerth balance (staking and dex `fee` fields reserved).
Every proof fixture was re-recorded.

## 2026-10-03: stake votes without spending (f2b38b9, 96c06a0)

MsgStakeVote used to spend and re-mint the voting note, so a note could vote
on only one of several open proposals (a decoy proposal could soak up votes).
**Rejected:** revealing the note's spend nullifier at each vote (it links the
votes to each other and to the later spend). **Chosen:** prove the note
unspent at the snapshot by non-membership of its nullifier in an indexed
(sorted) stake nullifier tree, and publish a per-proposal vote nullifier.
New `vote` circuit (9,046 gates).

## 2026-10-03: note value bound (audit 5)

Wallets hold values only up to 2^63−1, so every circuit range-checks note
values to 63 bits (action 8,120 -> 8,098; stake 9,647 -> 9,672; vote 9,046 ->
9,072) and every chain mint refuses more; large LP payouts split into notes.
Genesis sha256 `77af758697b293eb95d1f9f08a8f49b35bef648fd931ae1448cc3d0f2ddd652d`.
The referral note moved from the registrant's wallet to the chain (derived
opening). **Rejected:** checking a wallet-supplied pc against an opening in
the msg (more fields, same disclosure), and drawing the same total with or
without a referrer (kills the incentive).

## 2026-10-03: staking without background transactions (42a28e4)

User decision: no background transactions and no automatic fee spending.

- **Undelegations pay out by themselves.** MsgUndelegate names a pool pc and
  ciphertext; the chain mints the payout after maturity. MsgClaimUnbonding,
  the `unbond/` claim notes and fee-from-output were retired.
- **One stake vote per person.** One msg votes up to four notes with one
  rounded weight (27,543 gates, 2^15). Five slots would need 2^16, beyond the
  bundled SRS. **Rejected:** several validators in one msg (publishes and
  links the same per-validator weights); folding positions into the note vote
  (ties hidden notes to a public position).

Genesis sha256 `1824225dce524e47bf84bc5ff4fe0f8e76127b1c128480c925f6e420a6067ee8`.

## 2026-10-03: private redelegation (6918d7f)

The one exception to the feature freeze. First version: the module called
x/staking's BeginRedelegate, value left the source's queue first, and a slash
of the source fell on the destination's book. **Rejected then:** a dedicated
circuit minting the destination note (the rate moves every block; the proof
would be stale or the chain would recompute the amount anyway), and A's book
absorbing the slash (the redelegator escapes, A's remaining holders pay, and
x/staking caps the burn at A's remaining tokens). Accepted then and removed
later: griefing through x/staking's transitive rule and max_entries (one
person's move into A locked every private staker of A for 21 days). The
proposed fix, "lanes" (several module delegator accounts per validator),
touched every place the module reads its delegation and was never built.
Genesis unchanged (`1824225d…7ee8`).

## 2026-10-04: one stake note per validator, note-enforced slash debt (06fd915)

User decisions: staking more with a validator merges into the existing note;
redelegation slashes are paid by the notes the redelegation credited.

- **The chain mints no stake note.** Credits are public `v_in` merged by the
  proof. Weighed: **homomorphic credit** (Pedersen amounts in stake
  commitments: EC arithmetic in two circuits, a new note format, a public
  bound on the hidden amount; no privacy gain) and **an epoch-fixed rate**
  (a delegation just before the epoch end would buy rewards it did not earn).
  **Chosen:** the wallet names the credit and the chain checks the price.
- **No BeginRedelegate.** The module unbonds and delegates with x/staking's
  primitives and records the entry itself: no transitive lock, no
  max_entries. The credit carries a slash label; a slash debt tree records
  what each slashed move is still worth.
- **Labels stay in place.** **Rejected:** letting labels travel with chained
  moves (the debt would sit in one book and the paying note in another; the
  settlement would publish the label or the note's haircut).
- **Votes** keep the snapshot rule; **rejected:** letting a merged note carry
  the old value's vote (needs a lineage proof and links the notes).
- Circuits: stake v2 (two lanes, 16,242 gates, 2^14), vote v2 (2 slots,
  21,716 gates, 2^15).

Genesis sha256 `ffb269c5047e823b3f3aa27034767ff894c9fe76626703ccf42d0b1b321b6b59`.

## 2026-10-04: audit 7 staking fixes (17224a6 .. c563a2a)

A redelegation leaves the source's book pro rata (the queue-first rule let a
mover escape a coming slash); entries at the pair cap merge the two oldest
instead of joining the latest; the slashed entries are found by replaying
x/staking's slash; zero-height exports drop open moves; every stake proof
names the current clear_before and debt root. No circuit change; genesis
unchanged.
