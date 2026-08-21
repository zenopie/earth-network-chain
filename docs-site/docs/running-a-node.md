---
sidebar_position: 6
---

# Running a node

Anyone can run a node. No permission, no stake required.

The operational guides live with the code so they stay correct as the software
changes:

- **[Running a node](https://github.com/zenopie/earth-network-chain/blob/master/docs/JOIN.md)**
  — binary, genesis and its checksum, seeds, gas price, pruning, hardware.
- **[Upgrades](https://github.com/zenopie/earth-network-chain/blob/master/docs/UPGRADES.md)**
  — how a coordinated upgrade runs, and what goes wrong.
- **[Releases](https://github.com/zenopie/earth-network-chain/releases)** —
  binaries and checksums.

## What is different about this chain

Every registration verifies a zero-knowledge proof **on-chain**. That is unusual,
and it is CPU-heavy.

Two consequences:

- Budget more processor than a chain of this size would normally need.
- Use **state sync** rather than replaying from genesis, because replaying
  re-verifies every proof ever submitted. Sync cost grows with adoption, not just
  with time.

## Becoming a validator

There is no allowlist. Acquire ERTH, self-delegate, and submit
`MsgCreateValidator`.

There was no genesis allocation for validators. ERTH is earned — by registering a
passport, by bidding in the liquidity auction, or on the market.

Keep your consensus key off the node itself. The join guide covers the remote
signer setup, which fails closed: with a signer configured and none answering,
your node signs nothing rather than signing with a key it should not hold.
