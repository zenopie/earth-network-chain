# v0.7.0

**Status: implemented on `upgrade/v0.7.0`. Not tagged, not proposed.**

Implemented as the merge of this brief with the tagged-but-never-proposed
v0.6.1, so the chain takes one governance round trip rather than two. That is
only legitimate because no `MsgSoftwareUpgrade` named `v0.6.1` ever existed on
`earth-1`: an upgrade name nobody voted on is not a promise, and nothing has to
replay it. The v0.6.1 entry has been removed from `Upgrades`.

See the CHANGELOG entry for the operator-facing version. What follows is the
brief as written before implementation, annotated with what was found.

Consensus-breaking, so it goes through governance as a `MsgSoftwareUpgrade`
named `v0.7.0` — see [No in-place upgrades](../CHANGELOG.md) and the contract
on `Upgrade` in `app/upgrades.go`. Nothing in here may be shipped by restarting
validators on a new binary.

This document is the implementation brief. It exists because the three items
below were found in the 2026-08-28 audit and deliberately deferred rather than
hot-fixed: two are chain halts that are not yet armed, and the third gets more
expensive the longer it waits but is not exploitable at all. Each section says
what is wrong, why it was deferred, and what "done" means.

Two of the three are the same shape — **a running sum with a single maintainer,
bypassed somewhere else**. Read the pre-flight section before starting, because
there is a third instance of that pattern nobody has looked at yet.

---

## Pre-flight result: `LpTotalVolume` is sound — nothing to fold in

**Audited. No third instance of the pattern.** Recorded here because a negative
result that is not written down gets re-derived.

- `setPoolVolume` (`x/dex/keeper/lp_rewards.go:208`) is the only writer, and the
  only assignment to `pool.VolumeWeight` outside it is genesis's nil-guard and
  the two `math.ZeroInt()` initialisers in `CreatePool` and the auction, which
  move the total by zero.
- Both `applyVolume` call sites (`msg_server_swap.go:205,234`) persist the pool
  with `SetPool` immediately afterwards, inside the same atomic scope, so the
  in-memory pool and the global denominator cannot part ways.
- `InitGenesis` rebuilds `LpTotalVolume` by summing the imported pools
  (`x/dex/keeper/genesis.go:40`) — exactly what `x/allocation` was failing to do
  for `SummedAccrued`, which is item 2.
- `AssertInvariants` already runs `CheckVolumeAccounting` after every operation
  the tests perform, and the reasoning for keeping that O(pools) walk off the
  hot path (`invariants.go:319`) is sound.

The third running sum nobody had named is **`TotalPoolErth`**, and it is sound
for the same reasons: `SetPool` is its only writer, it self-heals on import
because it reads the previous record (absent on a fresh store), and
`CheckErthTotalAccounting` walks it in `AssertInvariants`.

So `x/dex` already has the discipline `x/allocation` was missing, which sharpens
item 1: the bug there was never the arithmetic, it was that `AssertInvariants`
omitted `CheckSolvency` while the dex's includes both walks.

<details>
<summary>The original pre-flight instruction, for reference</summary>

## Before you start: audit `LpTotalVolume`

`x/dex/keeper/lp_rewards.go` maintains `LpTotalVolume` the same way
`x/allocation` maintains `SummedAccrued` and `SummedWeight`: a running total
kept in step by one writer, standing in for a walk that would otherwise be
O(pools) every block. `CheckVolumeAccounting` (`x/dex/keeper/invariants.go`)
exists precisely because drift there is silent and costs real ERTH — the module
either mints more than the allocation stream released, or strands part of what
it did.

That file was **not** audited. Both bugs below are bypasses of exactly this
pattern, and the cost of finding a third one after shipping the fixes for the
first two is another upgrade cycle. Check every writer of `LpTotalVolume` and
every path that removes or rewrites a pool without going through the setter,
and fold the result into this release.

---

## 1. `x/allocation`: pruning an option halts the chain

**Severity: high. Permissionlessly armable on a 30-day fuse.**

`SummedAccrued` — the running sum of every option's `Accumulated` — is written
in exactly one place, `setOption` (`x/allocation/keeper/allocation.go:113-122`).
`SweepPrunableOptions` does not use it. It removes the option record directly
(`prune.go:167`) and then burns the option's forfeited balance
(`prune.go:185`):

    if err := k.Options.Remove(ctx, kk); err != nil {   // bypasses setOption
        return err
    }
    ...
    if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, burned); err != nil {

So the module's balance falls by `forfeited` while `SummedAccrued` keeps
counting it. `CheckSolvency` (`invariants.go:240`) computes
`owed = accrued + residue`, sees `held < owed`, and sets `Short`.
`AssertHotInvariants` turns that into an error. The sweep runs in **BeginBlock**
(`abci.go:28`) and the assertion in **EndBlock** (`x/allocation/module/module.go:153`),
so the halt lands in the same block as the prune.

Arming it needs no privileges. Adding an ADDRESS option is permissionless: add
one, vote for it long enough to accrue a balance, stop voting, never claim, and
wait `OptionIdleGrace` — 30 days (`x/allocation/types/keys.go:135`). It also
fires by accident the first time a real option is abandoned with an unclaimed
balance, which is the likelier way it happens.

The burn was added *for* solvency. `prune.go:171-174` says so: the coins exist,
so they are burned "rather than merely reported, which is what keeps the
module's balance equal to what its live options are owed." It does the balance
half and not the ledger half, and so creates the shortfall it was written to
prevent. The note at `prune.go:43-44` — that the stream's accounting is
untouched because the invariant is over `AmountAllocated`, which is zero on
anything prunable — is correct about the **weight** invariant and does not
notice that solvency is a second invariant, over `Accumulated`, which is
exactly what is non-zero here.

### Why the tests pass

This is the part worth understanding before writing the fix, because it will
recur otherwise.

`prune_test.go:114-133` does the right thing: it gives the option a positive
`Accumulated`, prunes it, and asserts invariants. The stub bank is honest —
`MintCoins` credits, `BurnCoins` debits, `GetBalance` reads
(`allocation_options_test.go:60-99`) — so the balance really does fall in the
test. It passes anyway, because `AssertInvariants` (`invariants.go:167`), the
function the tests call, checks **only the two weight invariants**.
`AssertHotInvariants`, the function the EndBlocker calls, checks solvency
first.

The suite's "assert after every operation" discipline has a hole in exactly the
place the chain halts.

### Done means

- `SweepPrunableOptions` decrements `SummedAccrued` by `forfeited` before the
  burn, reading it through `GetSummedAccrued` and writing it back.
- `AssertInvariants` includes `CheckSolvency`, so `prune_test` covers this
  without a new test and the next `setOption` bypass cannot land the same way.
  This is the more important half: the fix is three lines, the missing coverage
  is what let it ship.
- A regression test that prunes an option with a balance and asserts the
  **hot** invariants specifically.

---

## 2. `x/allocation`: solvency goes blind after a genesis round-trip

**Severity: medium. Same root cause as (1).**

Neither `SummedAccrued` nor `Residue` appears in
`proto/earth/allocation/v1/genesis.proto`, and neither is restored in
`x/allocation/keeper/genesis.go`. `InitGenesis` rebuilds `SummedWeight` by
summing the imported options (`genesis.go:135-159`) and does not do the same
for `SummedAccrued`.

After any export/import — a relaunch, or an upgrade that round-trips genesis —
`SummedAccrued` reads zero while the options carry real `Accumulated` balances
and the module account holds the coins backing them. `owed` collapses to
roughly `residue`, `held` stays where it was, and because
`SolvencyReport.Broken()` tests only `Short` (`invariants.go:221`) the gap
reads as a tolerated surplus. The check stays green while being blind to a
genuine shortfall up to the size of the un-imported accrued balance.

`Residue` has a second consequence: the coins sit in the module account but
`SweepResidue` moves nothing, so emission earmarked for the community pool is
stranded.

### Done means

- `InitGenesis` sums `opt.Accumulated` into a `summedAccrued` alongside the
  existing `summed`, in the same loop. It is derived state, like `SummedWeight`,
  so this needs no proto change and nothing new in `ExportGenesis`.
- `Residue` is **not** derivable and does need a genesis field, on both the
  export and import sides.
- Genesis validation asserts the imported options' accrued sum against the
  module's funded balance, the way it already checks weight.

---

## 3. `x/dex`: one bad unbonding entry halts the chain permanently

**Severity: high. Reachable through genesis, not through a message.**

`SweepMaturedUnbondings` returns on the first `payoutUnbonding` error
(`x/dex/keeper/lp_unbonding.go:60-67`), and `x/dex/module/module.go:149`
propagates that out of `EndBlock`. EndBlocker errors are not recovered by
baseapp — CometBFT treats them as fatal, and since every validator computes the
same state it is a chain halt rather than one node crashing.

It is **permanent**. The entry is removed only *after* its payout succeeds, so
it fails again next block. The queue is keyed by completion time and the loop
breaks at the first entry not yet due, so the bad entry sits at the head and
freezes every withdrawal behind it. Only an upgrade clears it.

The function's own doc comment argues the opposite — "the remainder is not
stranded, since the next block resumes from the oldest entry." That is true of
the `LpUnbondSweepLimit` cap it was written about and false when the oldest
entry is the one erroring.

### Ruled out — do not re-chase

Both of these were checked and are unreachable in normal operation:

- `ErrInsufficientPool` (`:105`) cannot fire: shares are escrowed on the module
  account rather than burned, so supply is always at least the entry.
- `k.Pool.Get` (`:90`) cannot fire: nothing anywhere removes a `Pool`.
  `SweepStalePools` only zeroes `VolumeWeight`.

### The way in is genesis

`GenesisState.Validate()` (`x/dex/types/genesis.go`) validates `PolBurns`
against `poolIndexMap`, with a comment saying a schedule with no pool "would
either panic the EndBlocker or retire the position at double speed." It has
**no validation whatsoever** for `gs.LpUnbondings`, and `keeper/genesis.go:54-62`
checks only that the address decodes. So a genesis can carry an unbonding with:

- a `PoolId` that does not exist — `Pool.Get` errors, halt when it matures;
- a nil `Shares.Amount` — `entry.Shares.Amount.GT(total)` at `:105`
  dereferences a nil `big.Int` and **panics**, which is worse than the error
  path because the node dies without saying why;
- a `Shares.Denom` that is not `LPShareDenom(PoolId)` —
  `MsgRemoveLiquidity` checks this at `msg_server_remove_liquidity.go:33` and
  genesis import does not. It burns the wrong pool's shares while paying from
  the right pool's reserves, then trips `AssertHotInvariants` pointing at the
  wrong module.

Pools have the same gap: reserves are never checked non-nil or positive, and
`:111` does `Mul(pool.ReserveErth.Amount)` unguarded — while
`CheckVolumeAccounting` (`invariants.go:186`) *does* guard
`VolumeWeight.IsNil()`. The codebase already knows nil `math.Int` from genesis
import is a live hazard for `Pool` fields; this path does not.

This matters because earth-1 relaunched from a fresh genesis on 2026-08-28 and
upgrades round-trip through `ExportGenesis`/`InitGenesis`.

### Done means

- The sweep survives a bad entry: log it, emit an `lp_unbond_payout_failed`
  event naming the pool, provider and shares, and `Remove` the key regardless.
  Dropping beats retrying — a retry changes nothing, the escrowed shares stay
  on the module account where `CheckShareBacking` reports them, and the
  solvency invariant still catches anything that actually moved coins wrongly.
  The pattern is already in the tree twice: `SweepStalePools`
  (`lp_rewards.go:411`) and `sweepExpiredRegistrations`
  (`x/personhood/keeper/registration.go:417-423`).
- `GenesisState.Validate()` gains an `LpUnbondings` block mirroring the
  `PolBurns` one: pool exists, shares non-nil and positive, denom equals
  `LPShareDenom(PoolId)`.
- The pool loop in the same function rejects nil or non-positive reserves.

**Leave `AssertHotInvariants` alone.** Its halt is deliberate and the reasoning
in `x/dex/keeper/invariants.go` is sound — the module is already wrong, and a
slow silent drain of the pre-mine is worse than a recoverable stop. It is easy
to conflate with this bug and "fix" by mistake. What is wrong here is a halt on
a single malformed row, before any of that, with no diagnosis.

---

## 4. `x/pki`: DSC commitment has no curve tag

**Severity: low, and not exploitable. Included because the cost only grows.**

`certs.DscCommitment` (`x/pki/certs/commitment.go`) and its Noir twin
`poa_core::dsc_commitment` (`circuits/poa_core/src/lib.nr:78` in
earth-network-mobile) hash a public key with no tag saying which curve produced
it.

Length is **not** the problem — the Poseidon2 sponge absorbs `len << 64` into
the capacity slot, so different-length inputs are already separated. The real
ambiguity is same-length keys on different curves: **P-256 ↔ brainpoolP256r1**
(64 bytes each) and **P-384 ↔ brainpoolP384r1** (96 each). RSA is 256 or 512
bytes and collides with nothing. The tag therefore has to identify the curve,
not merely RSA-vs-EC. One field element absorbed first is enough, on both
sides.

It is not exploitable: it needs two certificates that a trusted CSCA actually
signed whose coordinates coincide, and nobody chooses what a CSCA signs.

### Why it cannot wait indefinitely

The chain stores the commitment in `Registration.dsc_key` and never the public
key behind it, so existing registrations cannot have new-format commitments
recomputed — the input is gone. Every registration made before the fix is one
that a change of format strands. There was **1** registration on earth-1 when
this was deferred on 2026-08-28. Check the current count before deciding; past
some threshold this stops being a fix and starts being a migration nobody can
perform.

**Resolved: nothing is stranded.** The count was still 1 at implementation. The
input is gone *from state*, but not from history: the Document Signer's
certificate travelled in the `MsgRegister` that created the registration
(`earth-1` block 4833, algorithm `lean_poa_rsa2048`). It is embedded at
`app/upgrades/v070/dsc-certs/`, and the handler rewrites the commitment in all
six keyspaces that use it as an identity — the registration record, `RegByDsc`,
`RegCountByDsc`, `DscRate`, `PendingDscPurge`, and x/pki's
`RevokedDscCommitments`. A registration whose `dsc_key` no embedded certificate
reproduces makes the handler refuse to run rather than strand it.

This escape hatch does not scale. It works at one registration and would be
unreasonable at a thousand.

### Done means

Both implementations change **identically**, or every registration breaks — the
chain checks `commitment(cert key) == circuit output`. Then, in order:

1. Recompile all seven circuit variants with `nargo` 1.0.0-beta.22:
   `lean_poa`, `lean_poa_p384`, `lean_poa_brainpool256`,
   `lean_poa_brainpool384`, `lean_poa_brainpool512`, `lean_poa_rsa2048`,
   `lean_poa_rsa4096`.
2. Regenerate seven verifying keys with `bb` 5.0.0. **These are governance
   params** (`params.VerifyingKeys[algorithm]`).

   **Correction, from implementing it:** a separate parameter-change proposal is
   not merely inconvenient, it is unsafe. A param change executes when its
   proposal passes; the binary swaps at the plan height. Between the two,
   registration is broken in *either* ordering — a new-circuit proof verifies
   against a new key and then fails the commitment comparison against the old
   binary, and an old-circuit proof fails the new key outright. The keys are
   therefore written by the upgrade handler (`v070SwapVerifyingKeys`), so both
   flip in one state transition and the release stays a single proposal.

   The new keys live in `app/upgrades/v070/verifying-keys/` and are embedded.
   `networks/genesis/verifying-keys/` and `networks/genesis.json` are **not**
   regenerated: those are what block 0 installs, and the genesis hash is what
   `RESET_ON_GENESIS_MISMATCH` is keyed to.
3. Regenerate the real-proof fixture at
   `x/personhood/keeper/testdata/lean_poa`.
4. Replace the ~14MB of compiled circuits shipped in the Android app and the
   iOS equivalent.

Keep `bb` in lockstep with the vendored verifier
(`third_party/barretenberg-go/checksums.json`, pinned to `v5.0.0`).

---

## The upgrade entry

Add one entry to `Upgrades` in `app/upgrades.go`, named `v0.7.0`, following the
shape of `v0.6.1`. Notes on the fields:

- **StoreUpgrades**: none. The module set does not change.
- **AppVersion**: stays at 1, for the reason `v0.6.0` gives — it is pinned to
  `networks/genesis/chain.json`, which `x/upgrade` does not move, and editing
  that file changes the genesis hash `RESET_ON_GENESIS_MISMATCH` is keyed to.
- **CreateHandler**: the default handler is probably not enough. Items (1) and
  (3) both add rules that existing state could already violate, and state keeps
  moving between writing this and running it. Follow the `upgradeV061`
  precedent and assert the preconditions at the upgrade height rather than by
  hand beforehand:
  - no `LpUnbonding` in state whose pool is missing, whose shares are nil or
    non-positive, or whose denom does not match its pool;
  - `SummedAccrued` equals the walked sum of every option's `Accumulated`, and
    the module's balance covers it plus `Residue`.

  Both are expected never to fire. Failing at a scheduled height with everyone
  watching beats failing from an EndBlocker at an arbitrary one.

## Ordering

Items 1-3 are independent of each other and of item 4. Item 4 is the one with
an external dependency — the mobile apps ship the circuits, so a release that
changes the commitment format needs the app update in users' hands before or
with it, or registration breaks for anyone who has not updated. If that
sequencing is not comfortable, split item 4 into `v0.7.1` and ship the two
halts now; they are the ones with a real failure mode.

## Release checklist

- [x] `LpTotalVolume` audited (pre-flight above) — sound, nothing to fold in
- [x] `AssertInvariants` includes `CheckSolvency`, and walks the accrued sum
- [x] Regression tests for the prune halt and the unbonding halt, asserting the
      **hot** invariants
- [x] Genesis validation covers `LpUnbondings` and pool reserves
- [x] `Residue` added to the allocation genesis proto, both directions
- [x] `v0.7.0` entry in `app/upgrades.go` with its preconditions, and the
      `v0.6.1` entry removed
- [x] CHANGELOG entry, written for an operator deciding whether to restart
- [x] Item 4: seven circuits recompiled, seven verifying keys regenerated,
      mobile assets replaced, the single existing registration's commitment
      migrated rather than stranded
- [x] Verifying keys swapped by the handler rather than a param-change proposal,
      so the release is one proposal and has no broken window
- [ ] Android release built and distributed carrying the new circuits — **must
      be in users' hands at or before the upgrade height**, or registration
      breaks for anyone who has not updated
- [ ] iOS: still blocked on distribution; it reads circuits from the Android
      asset path and has no separate bundle to update
- [x] `v0.6.1` tag and GitHub release withdrawn, so nobody proposes a name this
      binary has no handler for — `v0.6.0` is Latest again, which is the release
      actually in its voting period
- [ ] Tag `v0.7.0` and let CI build the release binaries
- [ ] `MsgSoftwareUpgrade` proposed with name `v0.7.0` at a height that leaves
      operators time to build, after v0.6.0's height (30100) and **before the
      genesis liquidity auction is started**

## What was verified, and how

Each fix was confirmed to fail before it and pass after, rather than merely
passing:

- **Prune halt** — reintroducing the missing decrement makes the *pre-existing*
  `TestUnclaimedRewardsAreForfeitedWithTheOption` fail, which is what the brief
  predicted would happen once `AssertInvariants` walked the accrued sum. The
  dedicated `TestPruningAnOptionWithABalanceKeepsTheModuleSolvent` asserts
  `AssertHotInvariants` specifically.
- **Genesis blindness** — forcing `SummedAccrued` back to zero on import fails
  `TestSummedAccruedSurvivesAGenesisRoundTrip`.
- **Unbonding halt** — restoring the `return err` fails all three subtests of
  `TestOneBadUnbondingDoesNotHaltTheSweep`, including the nil-shares case that
  used to panic.
- **DSC commitment** — `TestDscCommitmentMatchesCircuit` passes for all seven
  variants against freshly generated proofs, so the chain's tagged commitment
  and each recompiled circuit's public output agree.
- **The remap** — `TestV070RemapReproducesTheLiveRegistrationsKey` pins the
  old commitment to the literal value in public signal [3] of earth-1 block
  4833, so an embedded certificate swap cannot go unnoticed.
- **Genesis untouched** — the `networks` package tests still pass, and
  `networks/genesis.json` hashes to the committed
  `7c7d9f25f842e36496fe6c00f9436b38f33dbad282a08fe2468d1a44b02be28d`.
