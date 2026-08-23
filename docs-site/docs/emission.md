---
sidebar_position: 3
---

# Emission

Earth issues **4 ERTH per second, forever**. Not a halving schedule, not a
governance parameter — a fixed rate.

Because the rate is constant while the supply it adds to keeps growing, the
inflation *rate* falls on its own: roughly 5% in year one, under 2.5% by year
twenty. There is no schedule to maintain and no halving to get wrong.

## The four pillars

That 4 ERTH/sec is split into **four equal pillars of 1 ERTH/sec each**. Two are
weighted by personhood and two by capital; two pay individuals and two pay
collectively-chosen destinations.

| Pillar | Weighted by | Paid to |
| --- | --- | --- |
| Staking rewards | capital | individuals, by stake |
| ANML buyback-and-burn | personhood | everyone, by burning ANML |
| Caretaker fund | personhood | options chosen one-human-one-vote |
| Groundworks fund | capital | options chosen by stake |

### Staking rewards

Standard proof-of-stake. Delegate ERTH to a validator, earn a share of 1
ERTH/sec proportional to your stake, minus that validator's commission.

Unbonding takes **21 days**.

### ANML buyback-and-burn

Every second, the chain mints 1 ERTH, buys ANML with it on the built-in exchange,
and destroys the ANML.

This is what gives ANML a floor. A registered human claims 1 ANML a day and can
sell it; the chain is a standing buyer funded by a quarter of all issuance.

### The two funds

The **Caretaker fund** and the **Groundworks fund** each direct 1 ERTH/sec to
options that holders choose.

The difference is who chooses. Caretaker is weighted **one human, one vote** —
holdings do not matter. Groundworks is weighted by **bonded stake**.

Anyone can add an option to either fund for a small fee. You split your vote
across options by percentage and change it whenever you like; rewards accrue
continuously to whatever you are currently pointing at.

Groundworks ships with an **emergency fund** option that pays the chain's
community pool. Stake pointed at it accrues ERTH into the pool every block, and
governance decides what the pool is spent on — so stakers can build a reserve
without knowing in advance what it will be needed for.

## The pre-mine

The chain started with **2,522,880,000 ERTH** — exactly twenty years of the
4 ERTH/sec rate, minted at genesis.

It is *additive*, not a substitute: the chain keeps issuing on top of it. At year
twenty the total is about 5.05 billion, not 2.52 billion.

It was split three ways, evenly:

- One third seeded the ANML/ERTH pool on the exchange.
- One third pays bidders in the liquidity auction.
- One third is paired with what those bidders raise, to open a second pool.

All of it went to liquidity. **Nobody received an allocation.**

## Protocol-owned liquidity is temporary

The chain owns that liquidity at the start, and **retires it to nothing over ten
years**.

Running a market is active management, and the incentives of a liquidity
provider are not the incentives of an ERTH holder. So the protocol seeds the
market and then gets out of the way, handing it to providers who will actually
manage it — and every year it steps back, their share of the rewards grows.

The ANML/ERTH position burns both assets as it retires. The auction pool burns
only its ERTH, leaving the other asset in the pool, which over time spends it
buying ERTH back off the market.

## Burning

Two things destroy ERTH continuously:

- **Half of every swap fee.** The fee is 0.3% per hop, charged in ERTH; half
  stays with the liquidity providers and half is destroyed.
- **Half of every transaction fee.** Gas is split the same way: half is
  destroyed, half is paid to validators and their delegators through the normal
  staking payout. Where the split cannot be even, the extra unit is burned.

So activity shrinks supply while issuance grows it, at a rate that does not
change.

The half that is paid out is the only part of a validator's revenue that
responds to how much the chain is used — the staking pillar is a fixed 1
ERTH/sec however busy the network is. That matters most for registration:
verifying a passport proof costs real CPU, and validators bear it on every
registration.
