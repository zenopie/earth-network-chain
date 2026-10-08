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

**Check first.** Whether waiting stake can be undelegated before it ever joins
(a deposit and an undelegation booked in the same epoch net in the sweep). If
so, a holder could collect a share of rewards without ever being exposed to a
slash, repeatedly. Quantify before choosing the fix.

**Intended fix (to design and audit).** A deposit enters a per-validator
*entry queue* outside the share pool: it holds ERTH, not derth, and is not in
`S_v` or `B_v` until the epoch end that delegates it, when it is credited derth
at that epoch end's rate (after that epoch's rewards and slashes are booked).
The note minted at deposit then has to carry the queued ERTH amount and be
converted to derth at its join epoch, or be minted at the join (a chain-minted
note, like payouts). Redelegation's destination leg (`redelegate.go`,
`PendingDelegation += r`) and genesis export/import of queued entries
(`export.go`) follow the same rule. Undelegating a still-queued deposit returns
its ERTH (no rate involved).

**Touches.** x/shieldedstaking (msg_server, epoch, rate, redelegate, export,
invariants, the stake note format and the stake circuit if a note can hold a
queued amount), the wallets (stake note valuation, the Stake tab's "waiting to
start earning" state, which then shows an ERTH amount rather than derth), the
backend indexer (stake note events), docs (staking). A stake-note format change
means a circuit change and a new verifying key in the upgrade.
