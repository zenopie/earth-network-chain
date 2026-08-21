---
sidebar_position: 5
---

# Governance

Anything that changes the rules goes through a vote. There is no admin key: the
authority for every privileged action is a module account **with no private
key**, and the only thing that can make it act is a proposal that passes.

## What governance controls

- Which countries' passport certificates the chain accepts, and revoking one that
  is compromised.
- The proof verifying keys.
- Every module's parameters — fees, unbonding times, emission destinations.
- Opening the liquidity auction.

## How a vote works

| | Deposit | Voting period | Yes needed |
| --- | --- | --- | --- |
| Normal | 1 ERTH | 7 days | two thirds |
| Expedited | 5 ERTH | 1 day | three quarters |

Both need **33.4% quorum**, and a **33.4% veto** defeats a proposal outright.
Voting power is bonded stake.

A two-thirds bar rather than a simple majority is deliberate. A rule that changes
on 50.1% is a rule that changes whenever the vote is close, and a chain whose
parameters can flip back and forth is not one anyone can build on. The expedited
track asks for more, not less: it trades a week of deliberation for one day, so
it should need broader agreement to make up for it.

The full deposit must be present when the proposal is submitted, or it waits in
the deposit period and the clock never starts.

## Revoking a compromised certificate

Governments sign passports with Document Signer certificates. If one is
compromised, someone could forge registrations with it, so revocation goes on the
**expedited** track — one day rather than seven.

Revocation is **not retroactive**. Registrations already made with that
certificate stay valid and are dealt with separately. Every registration names
its certificate publicly, so they can be found; wiping them all on a single vote
would punish people who did nothing wrong.

The [procedure](https://github.com/zenopie/earth-network-chain/blob/master/docs/TRUST_STORE_RUNBOOK.md)
is written down in advance, because an emergency is a bad time to be drafting.

## Where the chain is today

Earth launches with **one validator**. One party can therefore pass any proposal
— not through a special key, but by being the only staker.

Stated plainly rather than left to be discovered. It resolves as other people
stake, and there is no allocation list standing in the way of them doing so.
