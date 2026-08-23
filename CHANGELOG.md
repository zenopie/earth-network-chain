# Changelog

What changed in each release, for someone deciding whether to restart a
validator.

Entries are written for operators, not for the commit log. If a change means a
node behaves differently, needs a config edit, or must not be skipped, it says
so. The commit history has the reasoning.

This project follows [semantic versioning](https://semver.org). For a chain that
means: **any consensus-affecting change is breaking**, whatever the diff looks
like, because nodes running different versions cannot agree.

## [Unreleased]

The first release after this line is the launch candidate. Until `genesis_time`
is set and the gentx collected, the network is a single validator and there is
nothing to join.

### Breaking — consensus

- **`x/dex` checks its own books every block.** The EndBlocker asserts that what
  the module records and what it holds are exactly equal, and halts the node if
  not. Deliberate: a halt is recoverable by upgrade, a silent drain of the
  pre-mine is not.
- **`x/allocation` verifies each stream's weight every block**, with the same
  halting behaviour.
- **Creating a dex pool is refused until the genesis liquidity auction settles.**
  The auction has to be able to claim its bid denom and cannot defend it: the dex
  allows one pool per spoke token, `MsgStartLiquidityAuction` refuses to open when
  that denom already has a pool, and nothing can delete a pool — so a dust pool
  created beforehand would have blocked the auction permanently, and the proposal
  to open it publishes the denom a voting period in advance. The lock is blanket
  rather than denom-specific, needs nothing configured, and lifts itself when
  settlement creates the pool; from then on the ordinary one-pool-per-token guard
  protects that denom. A chain with no auction configured is never locked.
- **Protocol-owned liquidity is retired over ten years.** The genesis ANML/ERTH
  pool and the liquidity auction's pool were permanent; they now burn down on a
  straight line. ANML/ERTH burns both assets, the auction pool burns only ERTH.
- **The chain's own module accounts refuse ordinary transfers.** Sending to one
  was always a mistake, and it is now rejected rather than absorbed.
- **`x/pki` stores one record per certificate, not per signing key.** The trust
  store held 366 of 539 CSCAs; certificates sharing a key overwrote each other.
- **Genesis export carries the state it used to drop** — every registration and
  its nullifier, every revoked Document Signer, every allocation option and vote.
- **Block gas limit set to 100,000,000.** It was `-1`, meaning none.
- **Governance needs two thirds, not a simple majority.** `threshold` 0.5 → 0.667,
  and `expedited_threshold` 0.667 → 0.75 because the SDK requires the expedited
  bar to be strictly higher. Quorum and veto are unchanged at 33.4%.
- **Downtime tolerance raised to 10,000 blocks / 5%**, from the SDK defaults of
  100 / 50% — about four minutes, which with one validator meant a container
  restart halted the chain.

### Operators

- **The container joins a network instead of creating one.** It installs the
  genesis baked into the image, checks it against the published sha256, and
  starts. The old self-init behaviour is now `DEV_INIT=1`, and it makes a new
  chain every time.
- **`--api.enabled-unsafe-cors` is off by default.** Set `API_UNSAFE_CORS=1` if
  something needs the LCD from a browser. New `RPC_CORS_ORIGINS` takes a scoped
  allowlist for the RPC, which no deployment has ever had.
- **Minimum gas price is `0.005uerth`.** Transactions without `--gas-prices` are
  now rejected.
- **Binaries are published.** Linux amd64 and arm64, with checksums and the
  genesis file, on the releases page. Verified by a full dry run.
- **State-sync snapshots are on by default** — every 1000 blocks, keeping 5.
  The SDK default is off, and a snapshot cannot be made for a height already
  passed, so a chain launched without them leaves everyone who joins later
  replaying from genesis. `SNAPSHOT_INTERVAL=0` disables them, loudly.
- **Dead allocation options are removed.** An ADDRESS option that carries no
  weight for thirty days is deleted, and any ERTH it earned and nobody claimed
  goes with it — not burned, since an option's rewards are only minted when they
  are claimed. Anyone may trigger a claim on such an option and the payout goes
  to its recipient regardless of who sent it, so a live recipient has thirty days
  and a permissionless way to take what is theirs. Governance's INTEGRATED
  options are never touched. Capped at 20 removals a block; a quiet block reads
  one key.
- **The `Options` query is paged.** It returned every option in a stream on a
  route that costs the caller nothing, while the number of options is set by
  whoever pays the fee to add them. One request now returns at most 100. Clients
  that read the whole list must follow `pagination.next_key`.
- **The allocation invariant no longer walks every option.** It ran in the
  EndBlocker and summed each stream by decoding every option in it, while adding
  an option is permissionless — so the per-block cost of every node was
  something an outsider could raise for a one-time fee. The sum is maintained on
  write instead and the check compares two numbers per stream: 4,246 gas at five
  options and 4,246 at five hundred. It still halts on the drift it was added
  for, including the clamped case. The exhaustive walk moved to
  `AssertInvariants`, which tests run after every operation and operators can run
  against a node.
- **An option's description is capped at 256 bytes.** It had no bound of any
  kind, and adding an ADDRESS option is permissionless: about one ERTH of fee
  plus a fifth of one in gas bought a megabyte of text that every node then
  decoded in every block, since the weight invariant walks every option. The
  same bound applies to genesis import, so an exported file cannot carry what a
  message could not have created.
- **`tx allocation` names the streams correctly.** Its help said the stream
  argument was `human` or `capital`; both were renamed and neither is accepted,
  so anyone following it got a flag error. It is `caretaker` or `groundworks`.
- **Genesis funds no devnet account.** The ads-for-gas hot wallet is out; its key
  had been on a laptop. **It therefore has no funds at height 1** and must be
  funded after launch from the validator. Only the genesis validator and the dex
  module hold anything.

### Added

- **An emergency fund in the Groundworks stream.** A second genesis option
  (`#2`, `community_pool`) that credits its accrued ERTH to the SDK community
  pool every block, so stake can build a governance-spendable reserve. It has to
  be an INTEGRATED option: the community pool is x/distribution's `FeePool`, not
  a wallet, so an ADDRESS option paying the distribution account would raise a
  balance nobody can spend — and that account is blocked to payouts besides.
  Seeded at genesis rather than left to a proposal, so it is votable from height
  1. Chains importing an existing genesis do not get it and need a
  `MsgAddIntegratedOption`.
- `docs/JOIN.md` — running a node.
- `docs/UPGRADES.md` — coordinated upgrades, written from a rehearsal.
- `docs/TRUST_STORE_RUNBOOK.md` — revoking a compromised passport certificate.
- `scripts/build-genesis.sh` — the genesis is a build artifact now, not a file
  anyone edits.
- `scripts/rehearse-upgrade.sh` — runs a governance upgrade end to end locally.
- `LICENSE` — Apache 2.0. There was none, which legally meant nobody could use
  this.
- A documentation site at **[docs.erth.network](https://docs.erth.network)**,
  built with Docusaurus from `docs-site/`. Every page has an edit link, and the
  build fails on a broken link.

---

## [v0.1.6] and earlier

Released before this file existed. See the
[releases page](https://github.com/zenopie/earth-network-chain/releases) and the
commit history.

[Unreleased]: https://github.com/zenopie/earth-network-chain/compare/v0.1.6...HEAD
[v0.1.6]: https://github.com/zenopie/earth-network-chain/releases/tag/v0.1.6
