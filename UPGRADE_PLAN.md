# Next upgrade plan

Changes queued for the next earth-1 software upgrade. earth-1 is not upgraded in
place by hand: each item ships in a tagged release behind a governance upgrade
proposal, which needs the stake chamber and two thirds of the human votes cast
(x/assembly). Until humans can vote, an urgent fix is a coordinated halt-height
binary swap instead (deploy repo RELAUNCH.md, section 8).

Each entry: what is wrong, why it matters, the intended fix, and what it touches.

## 1. Waiting stake shares in rewards it did not earn (x/shieldedstaking)

**Found:** 2026-10-07, launch day, while explaining the Stake tab.

**What happens.** `MsgDelegate` credits derth at the live rate at once
(`msg_server.go`: `PendingDelegation += paid`, `DerthSupply += d`), but the ERTH
only joins the validator at the next epoch end (`epoch.go`, the book sweep). The
rate is `B_v / S_v` with `B_v = D_v + W_v + P_v − U_v` (`rate.go`). While the new
deposit waits in `P_v` it earns nothing, yet the rewards the already-delegated
`D_v` accrues into `W_v` raise the rate for every derth outstanding, the waiting
deposit's included.

Example: Alice has 1,000 ERTH delegated and earns 10 ERTH during the epoch. Bob
deposits 1,000 ERTH at its start. At the epoch end the 10 ERTH is spread over
2,000 ERTH of derth: Alice gets 5, Bob 5, though Bob's ERTH secured nothing.

**Size.** At most about one epoch (one day) of rewards per deposit, shared pro
rata; no principal is lost, existing holders earn less on days new stake
arrives. It does not arise while a validator has no delegated private stake
(the rate is 1 and there is nothing to share), which is earth-1's state at
launch.

**Why the live rate is used, and must stay for these cases** (`rate.go`): a
slash lowers `D_v` the moment it lands, so a stale rate would let the first to
see evidence exit at the pre-slash value; and rewards already accrued in `W_v`
must be priced in, or a deposit just before the epoch end would buy a whole
epoch's rewards. The fix must keep both properties.

**Checked (2026-10-08).** A deposit and an undelegation never net: the epoch
end delegates the whole queue, then undelegates the records' target from the
bonded stake through x/staking's full unbonding. Nobody can collect rewards
without staying staked; the issue is only the dilution above.

**Decided (user, 2026-10-08): bond a delegation in its own block.** The
queue was the only reason a deposit shared rewards it did not earn: priced at
the live rate and earning from the same block, it is exactly fair.
MsgDelegate's handler delegates the amount to the validator at once
(`bondNow`, x/staking's Delegate from the module, as a redelegation's arrival
already is); the rewards that delegation change pays out (x/distribution
withdraws them on every change) join the book's queue, so the backing grows
by exactly the amount and the rate does not move. A validator that cannot
take a delegation now (gone, or slashed to nothing) queues it for the epoch
end, as before. The epoch end still delegates the queue (rewards, a
redelegation's queued part). No note, circuit or wallet change: the wallet
still names derth at the live rate, with its quote margin (build 28).

Weighed and dropped: pending ERTH in the note plus an epoch rate tree (exact
but a circuit, note format and vote circuit change), the chain minting a
note at the join, and a projected epoch-end rate (an estimate).

Checked: a deposit bonded at once is exposed to slashes from that block;
leaving still takes x/staking's full unbonding; the live rate already prices
the rewards accrued so far, so nothing can be bought below its worth; the
deposit's amount and validator were public already.

**Touches.** x/shieldedstaking Delegate (`bondNow`), its tests and docs
(staking). Ships with item 2 in one upgrade (v1.2.1).

## 2. Groundworks votes by stake note, not positions (x/shieldedstaking, x/allocation, stake circuit)

**Status (2026-10-08):** built for v1.2.1 with item 1: the stake circuit
(Groundworks tags, no owner tag), the chain (votes keyed by tag, positions
deleted, the v1.2.1 handler installing the stake key), tests, both wallets
and the backend indexer (positions no longer indexed). Released first as
v1.2.0 (gov proposal 1), cancelled before it passed to ship moved stake
voting at once (below); v1.2.0 never ran. Ships as v1.2.1.

**Found:** 2026-10-08, from the wallet. A position is a separate, locked
record: stake in it cannot be moved or unstaked without an unlock, stake added
at the same validator does not join it, and wallets end up wrapping every
stake action in unlock / act / relock. Users read it as their stake being stuck.

**Decided (user, 2026-10-08):** simplicity over privacy, one transaction over
two. One stake note per validator (as now), and that note carries the vote.

**Design: a note votes in place, and every stake tx carries the vote forward.**

- **Stake circuit change** (new verifying key `stake`, set by the upgrade
  handler in x/shielded `verifying_keys`). A Groundworks tag per note that
  needs no tree position: `gw = H(TAG_GW, nk, rho)`. New public inputs:
  - `gw_0`, `gw_1`, `cr_gw`: each spent input's tag (padding: 0 or its own,
    as nullifiers are);
  - `gw_out`, `w_out`: the lane A output's tag and its unexposed amount
    (`out_amount - out_ex`), or both 0 when it does not vote;
  - `cr_gw_out`, `cr_w_out`: the same for the credit lane's output.
- **Votes**: `GroundworksVotes[gw] = {validator, weight, split, expires}`.
  - A stake msg may carry `groundworks_split`; with it, a nonzero `gw_out`
    (and `cr_gw_out`) is stored as a vote of `w_out` derth at that split.
    Without it both must be 0.
  - **Every stake tx cancels**: each revealed input tag removes the vote keyed
    by it. So every delegate / undelegate / move / merge ends the old note's
    vote and, when the wallet passes the split, starts the new note's in the
    same block. Every unit of derth is in one unspent note, so live weight
    never exceeds unspent voted derth.
  - Voting, changing the split, or renewing the lease alone: a restake of
    the note onto itself with the split (`MsgRestake` + `groundworks_split`).
  - The lease (`groundworks_lease_seconds`) runs from each vote; a stake tx
    that carries the vote forward renews it.
  - **Moved stake votes at once, pending** (2026-10-09, replacing "re-vote
    once the label clears"). A voting output's exposure (derth a move
    brought in, still labelled) is stored on the vote as `pending` with the
    move key and time, not counted. Lane A's is published by the proof
    (`p_key`, `p_time`, `p_ex`: the kept label's); the credit lane's is the
    credit itself. The chain adds `min(pending, the move's debt row)` (all
    of it if never slashed) to the vote's derth in BeginBlock once the
    move's window closes, at most 200 a block. No re-vote.
  - A reused `rho` gives two notes one tag: a second vote under a live tag is
    refused, and a spend only ever cancels the spender's own vote. Harmless.
- **Tally**: each vote's derth at its validator's live rate, as positions are
  weighed now, in that validator's one Groundworks voter beside its
  operator's self-bond. Slashes need nothing new.

**Positions are deleted, not migrated.** The only holder (the user) unlocks
before the upgrade. The upgrade removes `MsgLockPosition`, `MsgUpdatePosition`,
`MsgUnlockPosition`, `MsgPositionVote`, the position store, sequences and
events, and positions as a Groundworks weight source. The handler deletes
any position left (an upgrade handler that errors halts the chain, so it
does not refuse); earth-1 holds none (checked 2026-10-08, height 28665). Both wallets drop
every position screen, reminder and lease record.

**Privacy (accepted).** Per vote, as with positions: weight, validator and
split are public; the owner is not. New: a voter's stake txs at a validator
are linked into one pseudonymous history with amounts (each tx cancels the
previous vote and starts the next). Non-voters reveal a random-looking tag per
spend, which links nothing.

**Old wallets.** Their stake proofs fail against the new key from the upgrade
height. With one user, no dual-circuit transition: install the new build right
after the upgrade lands.

**Check first.** Genesis export/import of votes; the tally against the
one-voter-per-validator weighting (AUDIT_HISTORY G); gas for one KV delete per
input tag; that every stake msg's handler fixes the new public inputs (zero
where a msg may not vote: `MsgStakeVote`).

**Touches.** stake circuit (+ tools/privacyvectors), x/shielded verifying key
via the upgrade handler, x/shieldedstaking (msgs, vote store, cancel, tally
source, position deletion, export/import), x/allocation (Groundworks weight
source), proto, backend indexer, both wallets (prover, msgs, the
Groundworks screen), docs (emission, privacy, using-the-app).
