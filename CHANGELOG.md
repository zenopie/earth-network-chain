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

### Operators

- **Downloaded releases could not start.** Every published tarball up to and
  including `v0.4.5` contained `earthd` and `LICENSE` and nothing else, while
  the binary needs `libwasmvm` as a shared library. wasmvm's cgo directive bakes
  in `-Wl,-rpath,<the builder's Go module cache>`, which was the only rpath in
  the binary, so running it anywhere but the machine that built it gave

      libwasmvm.x86_64.so: cannot open shared object file

  The container image was never affected — it copies the library into
  `/usr/local/lib` and runs `ldconfig` — which is why this survived five
  releases. Anyone following `docs/run-a-node/join.md` hit it immediately.

  **The tarball layout has changed.** It now holds `bin/earthd` and a `lib/`
  with the matching `libwasmvm`, `libc++`, `libc++abi` and `libunwind`, and the
  binary carries `-Wl,-rpath,$ORIGIN/../lib` so it finds them wherever it is
  unpacked. `libc`, `libm` and the loader still come from the host, deliberately.

      sudo install -m755 <dir>/bin/earthd /usr/local/bin/
      sudo install -m644 <dir>/lib/*      /usr/local/lib/

  Both lines are required. `earthd` and `libwasmvm` are version-locked: never
  pair one release's binary with another release's library.

- **Cosmovisor is available, and optional.** It ships in the container image and
  is off unless `USE_COSMOVISOR=true`. Nothing changes for a node that does not
  set it. `docs/run-a-node/upgrades.md` documents the manual path and the
  cosmovisor path side by side.

  The release archive is rooted at `bin/` with no wrapper directory so that
  cosmovisor's auto-download works: it accepts `/<daemon>` or `/bin/<daemon>`
  and nothing else, and a wrapper directory fails at the upgrade height with the
  chain already halted.

- **The first real upgrade handler.** `app/upgrades.go` carries an entry for
  `v0.4.0`. Its migration does nothing, deliberately: this release needs no
  migration, which makes it the safest possible first exercise of a path that
  had never run outside `scripts/rehearse-upgrade.sh`.

  So there are two ways to take this release, and both are correct:

      in place       swap the binary. Nothing halts; the handler never runs.
      by governance  submit MsgSoftwareUpgrade for "v0.4.0" at a height. The
                     chain halts there, you swap the binary, it resumes.

  The second is a rehearsal with nothing at stake. Practise it here rather than
  on the release that finally does need a migration.

- `scripts/rehearse-upgrade.sh` no longer assumes `Upgrades` is empty. It
  needed a plan name the binary has no handler for, not an empty list — the old
  check would have retired the script permanently at the first real upgrade,
  which is precisely when rehearsing starts to matter.

### Breaking — clients, NOT consensus

- **Pool queries return real volume instead of an internal weight.** `GetPool`
  and `ListPool` now answer with `PoolView`, whose `volume_erth` is 14-day
  weighted swap volume in actual uerth. The stored `Pool.volume` is renamed
  `volume_weight` and no longer leaves the module; `last_volume_day` becomes
  `last_traded_day`.

  The old field was swap volume multiplied by a chain-wide index that grows
  7.7% a day forever, and the proto documented it as "decaying ERTH-denominated
  swap volume (~7-day half-life window)" — a description of the mechanism it
  replaced. The wallet implemented that description faithfully and its LP fee
  APR inflated with the index: right by accident on day one, 18x out within a
  month, 1,580x within three. Publishing a figure that has to be de-scaled
  before it means anything is what made that possible, so it is not published.

  The de-scaling happens at query time from state that already exists. Nothing
  new is stored.

  **This is not consensus-affecting and needs no coordinated upgrade.** The
  field numbers and types are unchanged, so stored bytes are identical and a
  new binary reads existing state exactly as the old one did; only the query
  layer moved. Nodes can be replaced in place, and a mixed network still agrees.

  Clients reading `volume` must read `volume_erth` and must NOT decay it — the
  chain has already done the weighting. `networks/genesis.json` changes, because
  its JSON carries field names; that matters only to a chain launching fresh
  from it.

### Breaking — consensus

- **ERTH and ANML now carry denom metadata.** Wallets and explorers read
  `bank.denom_metadata` to know that 1,500,000 `uerth` should be shown as
  `1.5 ERTH`. Without it Keplr and every explorer fall back to the raw
  micro-denom, which is what the running devnet does today —
  `/cosmos/bank/v1beta1/denoms_metadata` returns an empty list on it.

  `uerth` displays as `erth` (symbol ERTH, exponent 6) and `uanml` as `anml`
  (symbol ANML, exponent 6), matching the micro-unit convention used everywhere
  else in the repo.

  Genesis state, not a parameter: there is no `MsgSetDenomMetadata`, so a chain
  that launches without this can only get it through a governance proposal
  executing as the bank authority, or a relaunch. `validate-genesis` will not
  catch its absence either — the SDK validates metadata that is present and an
  empty list is perfectly valid — so `networks/genesis_test.go` asserts it
  instead.

  `dexlp/*` is deliberately excluded: LP share denoms are minted per pool at
  runtime, so there is no fixed set to declare at genesis.

  This also fixes `/cosmos.bank.v1beta1.Query/DenomMetadata` for contracts. The
  path was already on the CosmWasm query allowlist but returned NOT_FOUND,
  because there was no metadata behind it.

## [v0.2.1]

### Fixed

- **A devnet whose first boot was interrupted no longer crash-loops forever.**
  The `DEV_INIT` path in `docker/entrypoint.sh` writes
  `config/genesis.json` early and only collects the gentx several commands
  later. Anything that interrupted it in between left a genesis with an empty
  `gen_txs` on the volume, and because the resume path only checks that a
  genesis *exists*, every later start resumed that file and died with

      error during handshake: error on replay: validator set is empty after
      InitGenesis

  forever, with no way out but destroying the volume. The v0.2.0 lease hit
  exactly this: the pod reported available, never ready, and the Akash Console
  API exposes no logs to say why.

  The entrypoint now writes a `.devinit-complete` marker only after
  `collect-gentxs` succeeds, and treats a devnet genesis with no gentx as
  broken-by-construction — rebuilding it rather than resuming a chain that
  cannot produce a block. A healthy chain from an older image is adopted
  unchanged. `collect-gentxs` also no longer discards stderr, which is what made
  the original failure invisible.

- **The image now proves `earthd` runs before it ships.** A `RUN earthd --help`
  in the runtime stage forces the dynamic loader to resolve every NEEDED entry,
  libwasmvm included, so a binary that cannot start is a red build rather than a
  container that exits instantly somewhere you cannot read logs.

## [v0.2.0]

### Breaking — consensus

- **Permissionless CosmWasm.** The chain runs `x/wasm` (wasmd v0.61.14). Anyone
  may upload contract code and anyone may instantiate it, paying only gas —
  `code_upload_access` and `instantiate_default_permission` are both `Everybody`
  in genesis. Both are governance parameters, so they can be tightened later
  without a binary upgrade.
  - **Contracts can reach this chain's modules.** Messages go out as
    `CosmosMsg::Any` through the normal `MsgServiceRouter`, so a contract can do
    anything its sender could do and no more. Reads go through an allowlist in
    `app/wasm.go` — `/earth.personhood.v1.Query/Registration` above all, which
    is what lets a contract ask whether an address is a live verified human.
    Every path on that list is a promise not to change the response's wire
    shape, so it is deliberately short; paginated list queries are excluded.
  - **Contracts get IBC.** They can own ports (v1 and v2), and the callbacks
    middleware now wraps transfer and the ICA controller, so a packet can name a
    contract to invoke on receipt, acknowledgement or timeout.
  - **New store key `wasm` and a new module account.** The account holds the
    `Burner` permission only and is blocked from receiving transfers. Contract
    addresses are ordinary accounts and are not blocked.
  - **Operators: `libwasmvm` is now a runtime dependency.** The shared library
    ships in the container image; anyone building `earthd` themselves gets it
    from the Go module cache via the linker's rpath. A binary copied off the
    build host without it dies at startup with `libwasmvm.so: cannot open
    shared object file`.
  - **Operators: `app.toml` gains a `[wasm]` section** — `query_gas_limit`,
    `memory_cache_size`, `simulation_gas_limit`. Node-local, not consensus;
    nodes may differ without forking. A config generated by an earlier version
    has no such section and falls back to the compiled-in defaults.
- **The ante handler is now built in `app/ante.go`** rather than taken from
  x/auth/tx/config's default. Three wasm decorators are required for contracts
  to run at all, and two more were added that this chain should have had:
  - **The circuit breaker now works.** `x/circuit` was wired and its governance
    messages worked, but nothing consulted the tripped-message set, so
    "disable this message type" silently did nothing. The one lever for halting
    a misbehaving module during an incident was connected to a switch that was
    not attached.
  - **Redundant IBC relays are rejected**, so a relayer that loses a race pays
    no fee for the duplicate. Without it, relaying against earth costs more than
    relaying against anyone else.
- **`x/pki` can revoke a CSCA.** `MsgRevokeCsca` (governance) withdraws trust
  from a Country Signing CA, so no Document Signer chaining to it verifies from
  then on. `AddCsca` used to be a one-way door: a state in the trust store can
  sign as many Document Signers as it likes and each mints identities the chain
  counts as distinct humans, and the only answer was revoking those signers one
  at a time, after each was already in use.
  - What is revoked is the **signing key**, not the certificate handed in.
    Countries carry several CSCA certificates sharing one key — renewals, link
    certificates — and any of them verifies a child signature, so revoking a
    single certificate would have changed nothing.
  - **Prospective only.** Registrations already made keep claiming; retire them
    with `MsgRevokeDsc`, which carries the purge, or let them lapse within one
    `registration_validity_seconds`.
  - Re-adding the certificate with `MsgAddCsca` clears the revocation. That is
    the only way back — there is no un-revoke message.
  - New genesis field `pki.revoked_cscas`. Restored *after* the CSCA list,
    because replaying a CSCA clears its own revocation.
- **The trust store no longer carries Israel.** The three Israeli CSCAs ICAO does
  not distribute were removed from genesis, leaving the ICAO master list alone:
  539 CSCAs down to 536. Israeli passports cannot register — there is no
  ICAO-distributed Israeli CSCA to fall back on. Governance can add them back
  with `MsgAddCsca`; genesis was the only point at which they could be taken out.
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
- **Allocation emission is minted when it accrues, not when it is claimed.**
  `x/allocation` issues each stream's `1 ERTH/sec` into its own module account as
  the reward index advances, and every payout — option claims, the LP
  auto-compound, registration rewards, the community pool — is now a transfer out
  of it. Neither `x/dex` nor `x/personhood` mints allocation ERTH any more; the
  only ERTH minted outside `x/allocation` is the ANML buyback's own pillar, which
  is a separate emission and mints what it immediately spends. Reported supply is
  therefore what the chain owes rather than what has been collected, the emission
  rate can be checked against the block clock, and a new O(1) solvency invariant
  compares what the options say they hold against what the module is carrying. A
  stream with no votes mints nothing. Index truncation is swept to the community
  pool, where x/distribution already puts the dust from its per-validator split.
- **LP reward volume is scaled instead of decayed, and dead pools are swept.**
  A pool's volume was aged only when something touched it, while the denominator
  it was measured against kept the un-aged figure — so pools were credited less
  than the stream released on their behalf, and 9-11% of the LP emission went to
  nobody. Volume is now recorded multiplied by a global index that grows 14/13 a
  day (half-life ~9.4 days, twice the LP unbonding period), which produces the
  same weighting with nothing to age. Because scaled volume never reaches zero,
  trading starts a 60-day timer and a capped per-block sweep retires the weight of
  pools that stop trading, so a dead pool neither earns nor dilutes. The depth cap
  keeps its own 7-day window and is unchanged at 2x reserve per day.
- **The pre-mine splits four ways instead of three, and the registration-reward
  pool is pre-funded.** 630,720,000 ERTH each — five years of the whole chain's
  emission — to pool 1's reserve, both auction earmarks, and the human stream's
  option #1, which now starts with real coins on the `x/allocation` account
  (`allocation.registration_reward_seed`). The draw rate moves from basis points
  to parts per million and drops from 10 bps to 100 ppm, so the reward halves
  every 6,931 registrations instead of every 693 — $50 a side for the first
  registrant and their referrer at a $1M clear, still $18 a side at the
  ten-thousandth human. The finer unit exists because the unreferred branch
  halves the rate in integer arithmetic: in whole basis points the smallest
  usable rate was 2, since 1/2 truncated to zero and paid an unreferred
  registrant nothing without erroring. ANML's opening price is unchanged, since
  pool 1's ERTH side and the bidders' earmark move together.
- **The per-country daily registration floor drops from 10,000 to 1,000**, and
  the cap is now checked once, read-only, *before* the SNARK is verified rather
  than only after. A country sitting at its cap previously cost a full 4-6ms
  proof verification per rejected attempt; it now costs a map lookup. The
  authoritative check stays where it was — the country is only trustworthy once
  VerifyDsc has chained the certificate to a CSCA, so the early check runs after
  that and writes nothing, and cannot be used to exhaust anyone's allowance.
- **Protocol-owned liquidity is retired over five years.** The genesis ANML/ERTH
  pool and the liquidity auction's pool were permanent; they now burn down on a
  straight line. ANML/ERTH burns both assets, the auction pool burns only ERTH.
  Each quarter of the pre-mine is five years of the whole chain's emission and
  two of them sit in POL, so retiring them over five years burns 1,261,440,000
  ERTH against the pillars' 630,720,000 — supply falls by 630,720,000 over the
  window and only starts growing once the schedule is spent.
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
  Now also covers revoking a CSCA. Three things in it were wrong and are fixed:
  the revocation proposal named a `pubkey` field `MsgRevokeDsc` does not have,
  it pointed at an `earthd query pki dsc` command that does not exist, and it
  said revocation was not retroactive when it queues a purge that retires the
  signer's registrations.
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
