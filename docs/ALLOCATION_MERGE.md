# The two allocation streams, merged

Step 3 of the module reorganisation, **done**. Steps 1 (tokenomics into `x/earth`)
and 2 (`x/caretaker` → `x/personhood`) preceded it.

`x/personhood/keeper/allocation.go` and `x/deflation/keeper/allocation.go` were
near-duplicates of one engine, maintained twice — every fix so far (epoch-reset
semantics, the voter split cap, the zero-percent rejection) had to be applied in
both. They are now one module.

## What exists now

`x/allocation` owns both emission streams, keyed by a stream id:

```go
STREAM_ID_HUMAN   = 1 // one-human-one-vote, 1 ERTH/sec
STREAM_ID_CAPITAL = 2 // stake-weighted,     1 ERTH/sec
```

Every piece of state is stream-prefixed: `Options[(stream, id)]`,
`Voters[(stream, addr)]`, `RewardIndex[stream]`, `TotalWeight[stream]`,
`Epoch[stream]`, `OptionSeq[stream]`, `IntegratedOptions[(stream, id)]`. Option
ids restart per stream.

The streams differ in exactly one place, behind one interface:

```go
type WeightSource interface {
    // Weight returns the voter's current weight, or zero if they may not vote.
    Weight(ctx context.Context, addr []byte) (math.Int, error)
}
```

`x/personhood` registers the human source (live registration → `HumanVoterWeight`);
the capital source is internal to `x/allocation` (`GetDelegatorBonded`), since
that module already holds the staking hooks that keep the weight in sync.

`x/deflation` is gone. Its `lp_rewards` integrated handler is registered by
`x/dex`, and `registration_rewards` by `x/personhood`.

## Dependency direction

The registries (`RegisterWeightSource`, `RegisterIntegratedHandler`) are maps on
the keeper, populated from other modules' wiring. That is what keeps the graph a
tree: `x/allocation` depends on staking and bank and nothing else;
`x/dex` and `x/personhood` reach *into* it. A keeper-to-keeper dependency in both
directions would deadlock depinject.

`x/personhood` calls back through a narrow interface it declares itself
(`AdvanceIndex`, `ClearVoter`, `DrawFromOption`) rather than importing the
allocation keeper.

## Invariants worth not breaking

- **The two epochs are independent.** A governance reset of one stream must not
  touch the other. `TestResetAllocationsIsPerStream` pins this.
- **`MaxVoterOptions` is a DoS bound, not a usability rule.** `x/personhood`'s
  expiry sweep clears a lapsed human's vote from BeginBlock, unwinding their
  split one option at a time with nobody paying gas — multiplied by the sweep
  limit in a single block.
- **The human stream's weight is flat.** Bonded stake belongs to the capital
  weight source and the staking hooks, never to the shared engine.
- **Option ids are per stream.** `RegistrationRewardOptionID` and
  `LPRewardsOptionID` are both 1; under one module they would collide if ids were
  global.
- **`x/personhood` runs before `x/allocation` in BeginBlock.** The sweep has to
  return a lapsed human's weight before the stream settles the block's emission.

## Verification

Run the chain, not just the test suite. Three separate breakages during this
reorg passed all tests and would still have broken a running chain: missing
module account permissions, a renamed genesis key, and a mangled proto.

Minimum checks: bonded pool balance equals the sum of validator tokens at a
pinned height; `personhood params` returns 7 verifying keys; both streams accrue
and claim.
