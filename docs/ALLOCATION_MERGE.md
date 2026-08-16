# Merging the two allocation streams

Step 3 of the module reorganisation. Steps 1 (tokenomics into `x/earth`) and 2
(`x/caretaker` → `x/personhood`) are done and verified on a running chain.

## Why

`x/personhood/keeper/allocation.go` and `x/deflation/keeper/allocation.go` are
near-duplicates. Both implement the same engine:

| concern | personhood | deflation |
|---|---|---|
| advance index | `advanceDemIndex` | `advanceAllocationIndex` |
| settle option | `settleOption` | `settleOption` |
| resync voter | `resyncDemVoter` | `resyncVoter` |
| reward index | `DemRewardIndex` | `RewardIndex` |
| total weight | `DemTotalWeight` | `TotalWeight` |
| epoch | `DemEpoch` | `AllocationEpoch` |
| reset | `resetDemAllocations` | `resetAllocations` |

They differ in exactly two places:

1. **Weight source.** Personhood uses a fixed `VoterWeight = 100` per registered
   human (one-human-one-vote). Deflation uses bonded stake, normalized through
   `earthKeeper.NormalizeStakeWeight`.
2. **Who may vote.** Personhood requires a live registration
   (`requireValidHuman`); deflation requires positive bonded stake.

Everything else — options, voters, the index maths, epoch reset, claim, the
INTEGRATED/ADDRESS split, the `MaxVoterOptions` cap — is identical and currently
maintained twice. Every fix so far has had to be applied in both places: the
epoch-reset semantics, the voter split cap, the zero-percent rejection.

## Target

One `x/allocation` module owning two *streams*, keyed by a stream id:

```go
type StreamID uint32
const (
    StreamHuman   StreamID = 1 // one-human-one-vote, 1 ERTH/sec
    StreamCapital StreamID = 2 // stake-weighted,     1 ERTH/sec
)
```

All state becomes stream-prefixed: `Options[(stream, id)]`,
`Voters[(stream, addr)]`, `RewardIndex[stream]`, `TotalWeight[stream]`,
`Epoch[stream]`, `IntegratedOptions[(stream, id)]`.

Weight resolution moves behind one interface the module is constructed with:

```go
type WeightSource interface {
    // Weight returns the voter's current weight, or zero if they may not vote.
    Weight(ctx context.Context, addr []byte) (math.Int, error)
}
```

`x/personhood` supplies the human source (registration lookup → `VoterWeight`);
`x/earth` + staking supply the capital source (`GetDelegatorBonded` →
`NormalizeStakeWeight`). Both are one small adapter each.

## Order of work

1. Scaffold `x/allocation` with stream-prefixed state and the merged engine.
   Port the deflation version — it is the one with the epoch, cap and
   normalization fixes already in it.
2. Move the msg servers, collapsing each pair into one message carrying a
   `stream` field: `SetAllocations`, `ClaimAllocation`, `AddIntegratedOption`,
   `AddAddressOption`, `ResetAllocations`.
3. Move both integrated handlers: `lp_rewards` (→ `x/dex`) and
   `registration_rewards` (→ `x/personhood`).
4. Move the staking hooks (`AfterDelegationModified`, `BeforeDelegationRemoved`)
   — they only serve the capital stream.
5. Strip both old modules down; `x/deflation` disappears entirely.
6. Genesis: `config.yml` currently seeds allocation state under `deflation:` and
   `personhood:`. Both move under `allocation:` with stream ids.
7. Rewire depinject and `app_config.go`.

## Traps

- **`removeRegistration` calls the human resync from BeginBlock.** The expiry
  sweep unwinds a lapsed voter's split with nobody paying gas. `MaxVoterOptions`
  must survive the merge or that becomes an unmetered DoS again.
- **The two epochs are independent.** A governance reset of one stream must not
  touch the other. Keep `Epoch` per stream, not global.
- **The human stream's weight is constant.** `resyncDemVoter` takes no weight
  argument today; the merged version passes `VoterWeight` from the source. Do not
  accidentally normalize it by the stake compound index.
- **`x/personhood` registration rewards read option #1.** `RegistrationRewardOptionID`
  and `LPRewardsOptionID` are both 1 today, in different modules. Under one module
  they collide unless ids are per-stream.

## Verification

Run the chain after each step, not just the test suite. Three separate breakages
this session passed all tests and would still have broken a running chain:
missing module account permissions, a renamed genesis key, and a mangled proto.

Minimum checks: bonded pool balance equals the sum of validator tokens at a
pinned height; `personhood params` returns 7 verifying keys; both streams accrue
and claim.
