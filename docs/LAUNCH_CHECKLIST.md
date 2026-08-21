# Launch checklist

What has to change before this repo produces an actual chain release.

"Release" is two things here, and they fail differently. A **software release**
is a versioned `earthd` anyone can build, verify and run. A **network launch** is
one genesis file every node agrees on, with a validator set that can be joined.
Today the repo does the first halfway and the second not at all: the container
does not join a chain, it *creates* one on first boot.

Nothing below is a bug in the protocol. It is the difference between a devnet
that only ever talks to itself and a network other people can run a node on.

---

## 0. Decisions that must be made before genesis is frozen

These are choices, not work items. Every later phase depends on them, and none
can be changed after the file is signed and published.

- [ ] **The validator set at height 1.** Either collect gentxs from N independent
      operators, or launch single-validator and say so in public along with the
      plan for opening it up. Both are defensible; leaving it implicit is not.
      Note that `x/personhood` and `x/allocation` make this more than a liveness
      question — a single validator can censor registrations, which is the one
      thing the chain exists to make unforgeable.
- [ ] **The auction bid denom.** `deploy/genesis.json` ships the liquidity
      auction as `AUCTION_STATUS_PENDING` with `bid_denom: ""` and
      `end_time: 0`, holding 840,960 ERTH in each earmark. `MsgStartLiquidityAuction`
      is governance-only and, per its own comment, "only meaningful once IBC is
      live and the intended bid asset actually exists on the chain" — so the
      launch story is: chain starts, IBC channel opens, governance starts the
      auction. Write that sequence down before genesis, because 1.68M ERTH sits
      idle on the dex module account until it happens.
- [ ] **`min_deposit`.** `config.yml:56` still says `# TODO: raise before
      mainnet`, and 1 ERTH to propose is not a mainnet number. It is the one gov
      value that cannot be copied from another chain: it should be a meaningful
      fiat amount at ERTH's launch price.
- [ ] **A nonzero minimum gas price.** `MIN_GAS_PRICES=0uerth`
      (`deploy/docker/entrypoint.sh:19`, `deploy/akash/deploy.yaml:34`) is a
      devnet posture and is labelled as one. Free transactions on a public chain
      are free spam, and the mempool is the cheapest thing to attack. ads-for-gas
      is the answer to "users have no ERTH yet" — it is not an argument for a
      zero floor at the node.
- [ ] **Who holds the gov keys at launch**, given that `MsgAddCsca`,
      `MsgRevokeDsc`, `MsgUpdateParams` (verifying keys, `dsc_root`) and
      `MsgStartLiquidityAuction` are all governance-gated and all
      security-critical. With a small genesis validator set, "governance" is a
      short list of people; say who.

---

## 1. Genesis becomes an artifact, not a boot-time side effect

**This is the blocker.** `deploy/docker/entrypoint.sh:29-62` copies
`deploy/genesis.json`, rewrites `genesis_time` to `date -u` *now*, creates a
validator key, then runs `add-genesis-account`, `gentx` and `collect-gentxs`.
Every node that starts this image therefore computes a *different* genesis, a
different app hash, and cannot share a network with any other node. The committed
file confirms the shape: one account, `"gen_txs": []`.

- [ ] **Fix `genesis_time`** to a specific UTC instant in the future and stop
      stamping it at boot. The stamping exists for a real reason — emission is
      prorated against elapsed time, so a stale genesis pays the whole gap out at
      height 2 (`deploy/docker/README.md` records 125,485 ERTH minted in one
      block from a day-old file). The fix is a launch time close to when the
      chain actually starts, not a per-node rewrite.
      The POL retirement schedules do the same thing, for the same reason:
      launching from the committed file today retires 2,996,952,143 LP shares —
      4.4 days of the ten-year schedule — in a single block. Measured, not
      predicted.
- [ ] **Collect the gentxs** into the file, or ship zero and have validators join
      with `MsgCreateValidator` after height 1 — decided in phase 0.
- [ ] **Set `consensus.version.app`.** It is `"0"` today. It should be the
      launch app version, and it moves with each upgrade.
- [ ] **Drop the devnet accounts.** `earth1jtc2zjmmmyttdayz6aw8vfgt5qn4hg7rpxaar6`
      holds 10,000 ERTH and is the ads-for-gas hot wallet; `deploy/docker/README.md:64`
      already says both seeded accounts are "devnet keys with no value; drop them
      from `deploy/genesis.json` for anything real". They are still there.
      Replace with whatever the real operational funding is, from a key that
      exists in a signer nobody's laptop has seen.
- [ ] **Publish the sha256 of the final genesis** in the release notes, and have
      the entrypoint *verify* it rather than generate anything. New entrypoint
      logic: if `$EARTH_HOME/config/genesis.json` is absent, copy
      `/etc/earth/genesis.json`, check its hash against a baked-in constant,
      start. Otherwise start. Keep the current self-init path behind an explicit
      `DEV_INIT=1` so `scripts/testnet-3val.sh` and local devnets still work.
- [ ] **Stop using `--keyring-backend test` on the init path**
      (`deploy/docker/entrypoint.sh:24`). It writes the validator's operator key
      unencrypted into the volume. Related: `VALIDATOR_MNEMONIC` is injected into
      the submitted SDL and therefore reaches whoever hosts the lease
      (`deploy/akash/deploy.yaml:38-48` documents this as a deliberate devnet
      trade). Neither survives contact with a real validator key.
- [x] **Make genesis generation reproducible.** ~~Today it is `ignite chain init`
      followed by hand-stripping gentxs and dev accounts and "recompute bank
      supply".~~ Done: `scripts/build-genesis.sh` builds `deploy/genesis.json`
      from `deploy/genesis/` (see its README) and writes a `.sha256` beside it.
      Output is canonically sorted, so two people who run it get the same bytes.
      `make genesis-check` fails if the artifact drifts from its sources.
      `go test ./deploy/...` asserts `bank.supply == sum(balances)`, that no
      account outside `accounts.json` holds a balance, that the dex module's
      balance equals pool 1's reserve plus both auction earmarks, that the LP
      supply is `sqrt(reserve_erth * reserve_token)` and that the retirement
      schedule is sized to exactly that, that every verifying key and the CSCA
      trust store are seeded, and that `config.yml` has not drifted from the
      launch sources again (it had — the pre-mine split in `6dd49f3` never
      reached it).
      **Still open here:** `genesis_time` and the gentxs, both above.

---

## 2. There has to be a way to join

- [ ] **Expose p2p.** `Dockerfile` exposes only 1317 and 26657;
      `docker-compose.yaml` publishes neither 26656 nor gRPC. The entrypoint
      binds p2p on `0.0.0.0` but nothing can reach it. On Akash this is a
      structural SDL change and therefore a lease replacement — plan it with the
      genesis cutover, not after.
- [ ] **Publish seeds.** At least one seed node's `nodeid@host:26656`, in the
      README and in the release notes.
- [ ] **Turn off `--api.enabled-unsafe-cors`** for validator nodes. Any origin
      can currently read and broadcast. Keep it on a separate public LCD/RPC node
      if the web app needs it; it does not belong on a block producer.
- [ ] **Set snapshot and pruning defaults** so a new node can state-sync instead
      of replaying from genesis: `snapshot-interval`, `snapshot-keep-recent`, and
      a documented `pruning` profile per node role (validator, archive, public
      RPC).
- [ ] **Write the join procedure** as a single doc: binary + checksum, genesis +
      sha256, seeds, min-gas-price, pruning, hardware. If a competent operator
      cannot get a node syncing from that page alone, the network is not
      launchable regardless of the code.
- [ ] **Revisit the slashing window.** `signed_blocks_window: 100` with
      `min_signed_per_window: 0.5` jails a validator for 50 missed blocks in a
      100-block window — minutes of downtime. The Hub uses 10,000 / 0.05. Also
      check `historical_entries: 10000` on staking, which is 100x the SDK default
      and is state every node carries.

---

## 3. Binary release engineering

- [ ] **Publish binaries at all.** `.github/workflows/release.yml` is
      `on: workflow_dispatch` — it was disabled because it fired on every push,
      and the consequence is that tagging `v0.1.6` produces a Docker image and
      nothing else. A chain release needs per-platform binaries with checksums.
- [ ] **Build the image with the version ldflags.** The Dockerfile runs plain
      `go build ./cmd/earthd`, so `earthd version` in the shipped image reports
      nothing. The Makefile already computes the right `-ldflags`; the image does
      not use them. Operators use that string to answer "am I running what
      everyone else is running", which during an upgrade is the only question.
- [ ] **Stop pushing `:latest`.** `.github/workflows/docker-build.yml` pushes a
      mutable tag alongside the version. The SDL correctly pins digests; `latest`
      is a way for someone to skip that.
- [ ] **Reproducible builds.** cgo plus a prebuilt Aztec archive means two
      operators can plausibly end up with differently-behaving verifiers, which
      is a consensus fault, not a packaging annoyance. Add `-trimpath`, pin the
      toolchain, publish per-platform sha256, and make `verifier-libs.yml` the
      only source of `libbarretenberg.a` — verified against
      `third_party/barretenberg-go/checksums.json` at build time.
- [ ] **Get compiled artifacts out of the tree.** `poafixtures` (4.6 MB, a built
      binary) sits at the repo root, and `third_party/barretenberg-go/lib/darwin_arm64/libbarretenberg.a`
      is 52 MB of object code for a platform no validator runs. `poafixtures` is
      regenerable from `tools/poafixtures` via `scripts/regen-poa-fixtures.sh`
      and should not be tracked.
- [ ] **Add a `LICENSE`.** There is none. Without one the default is "nobody may
      use this", which is an odd position for a public chain.
- [ ] **Add a `CHANGELOG.md`** with a real entry per tag. The commit log is good
      but it is not what an operator reads before restarting a validator.

---

## 4. The upgrade path has to be exercised once

`app/upgrades.go` is well-built — named handlers, store loader, skip-height
handling — and `var Upgrades = []Upgrade{}` is empty, so none of it has ever run.
An untested upgrade path is discovered during the upgrade.

- [ ] **Rehearse a no-op upgrade end to end** on a testnet: submit
      `MsgSoftwareUpgrade`, halt at height, swap the binary, resume.
- [ ] **Ship cosmovisor** in the image with the standard layout
      (`$DAEMON_HOME/cosmovisor/genesis/bin/earthd`), so operators can stage the
      next binary rather than racing a halt at 3am.
- [ ] **Document the halt-height procedure**, including `--unsafe-skip-upgrades`
      and what to do when a validator restarts into the wrong binary.
- [ ] **Decide the store-migration policy** for the modules most likely to change
      shape — `x/personhood` params (verifying keys, signal indices) and
      `x/dex` auction state.

---

## 5. Correctness gates that are currently open

- [ ] **The simulation ops are stubs.** Every one of
      `x/dex/simulation/*.go` and `x/personhood/simulation/*.go` is a
      `// TODO: Handle the X simulation`, so `app/sim_test.go` exercises the SDK
      and nothing of this chain. These are the cheapest confidence available on
      the AMM's invariants and the personhood state machine, and they run in CI
      forever once written.
- [x] **No invariants are registered anywhere.** Done for `x/dex`, which is
      where the pre-mine sits. `x/dex/keeper/invariants.go` checks, every block
      in the EndBlocker, that the module's records and its bank balance agree
      **exactly** — both a shortfall (a withdrawal that will not be payable) and
      a surplus (coins that should have been burned and were not, which nothing
      else would ever notice). A breach returns an error from EndBlock, which
      halts the node; that is the intended outcome, since a halt is recoverable
      by upgrade and a silent drain of the pre-mine is not. A second check
      enforces the LP-reward denominator that `lp_rewards.go` had only asserted
      in prose. Share backing (escrowed withdrawals plus the protocol's own
      position against the shares that exist) walks the unbonding queue, so it
      runs in tests rather than per block.
      Verified by mutation: five separate injected bugs — a retirement that
      shrinks a reserve without burning, a swap fee deducted but left in the
      pool, an unbonding paid without shrinking the reserve, a reward credited
      twice, and double-counted escrow — are each caught with an exact figure.
      A live node ran 62 blocks from `deploy/genesis.json` with no breach.
      **Not done:** `x/allocation`'s reward index and `x/personhood`'s ANML
      accounting have no equivalent.
      **Note:** `x/crisis` is deprecated in SDK v0.53 and removed in the next
      release, so the checks are enforced directly rather than registered
      through it. This also required blocking the chain's own module accounts
      from receiving ordinary transfers (`app/app_config.go`) — without that,
      anyone could halt the chain with a `MsgSend`, and the check would have to
      weaken to "holds at least what it owes", which stops catching the second
      class of bug entirely.
- [ ] **Pin the public-input schema.** `docs/PROOF_OF_PERSONHOOD_TODO.md:27`
      records that `verifyRegistrationProof` still uses placeholder indices
      (`nullifierSignalIndex=0`, `dscRootSignalIndex=2`). These are consensus
      values baked into how every registration is checked; they cannot be
      "adjusted later" without invalidating everyone already registered.
- [ ] **Seed the real verifying keys and `dsc_root`** in genesis, per supported
      signature algorithm, from the final circuits.
- [ ] **Write the trust-store runbook.** The on-chain path exists —
      `MsgAddCsca` and `MsgRevokeDsc`, both governance-gated — but a CSCA
      rotation or a compromised DSC is a time-sensitive event, and a 7-day
      voting period is not a response time. Decide in advance whether revocation
      goes through the expedited track (1 day, 2/3) and who drafts it.
- [ ] **External audit** of `x/dex` (auction settlement and LP accounting),
      `x/allocation` (the reward index, shared by two streams) and the verifier
      shim in `zk/ultrahonk`. These are the three places where a bug is
      unrecoverable rather than embarrassing.

- [x] **Genesis export was dropping most of the chain's state.** `GenesisState`
      was params-only for `x/allocation`, `x/personhood` and `x/pki`, so
      `earthd export` → import silently discarded every registration (and with
      them the nullifier set, resetting the anti-Sybil property to "nobody ever
      registered"), every revoked Document Signer, and every allocation option,
      vote and reward index. Fixed: all three now carry their state, with derived
      indexes rebuilt at InitGenesis rather than exported, and `Validate` refusing
      a malformed file at import instead of halting the chain at height 1.
      `TestAppImportExport` passed throughout because the simulation ops are
      stubs, so the sim never created a registration, an option or a revocation —
      empty state round-trips perfectly. That is a concrete argument for the sim
      ops above.
- [x] **`x/pki` stored one certificate per signing key, not one per
      certificate.** `deploy/genesis.json` carries 539 CSCAs and the chain held
      366: `Cscas` was keyed by `cscaID`, which is the SKI, so certificates
      sharing a key overwrote each other and 173 certificate bodies were dropped
      at InitGenesis without ever reaching the chain.

      Measured first, because it decided the fix. Of 536 parsed certificates
      there are 366 distinct SKIs and 337 certificates sharing one — and **all
      337 share a public key**. None is a real collision; they are renewals and
      link certificates for one signing identity, and any of them verifies a
      given signature. Exactly one SKI group spans more than one subject DN.

      So keying issuer *lookup* by SKI was right — a DSC's AKI is its issuer's
      SKI — and the bug was that one map served as both the lookup index (per
      key) and the record store (per certificate). Now split:

          Cscas      certID (sha256 of DER) -> Csca
          CscaBySKI  (SKI, certID)
          CscaByDN   (sha256(DN), certID)

      `issuerCandidates` walks `CscaBySKI` under the DSC's AKI instead of doing
      one `Get`, so it returns every certificate carrying the key rather than an
      arbitrary sibling — they share a key but differ in validity period, and the
      caller can only pick the one that was valid if it is given all of them.
      A live chain now stores and exports all 539.

      No migration: the store layout changed, but the chain has not launched
      (`app/upgrades.go` is empty) and `deploy/genesis.json` is byte-identical,
      because the file always carried 539 — it was the import that collapsed
      them. A devnet with state worth keeping needs a restart from genesis.

---

## 6. Documentation the launch produces

- [ ] `docs/JOIN.md` — the operator page from phase 2.
- [ ] Release notes per tag: binary checksums, genesis sha256 (launch tag only),
      upgrade name and height (upgrade tags only).
- [ ] Replace the Ignite boilerplate in `readme.md:210-228`. It currently tells
      readers to `git tag v0.1` for a draft release that the disabled workflow
      will not create, and to install via `get.ignite.com/earth-network/earth`,
      which is not where this repo lives.

---

## What is already right

Worth stating so none of it gets "fixed" during the rush:

- The SDL pins the image by **digest**, and CI rewrites it on release
  (`deploy/akash/deploy.yaml:22-28`). Correct, and for the stated reason.
- Genesis state lives on a mounted volume, and the entrypoint distinguishes
  first boot from restart by the presence of `genesis.json` — the failure mode it
  avoids (a redeploy silently starting a different chain) is the right one to
  worry about.
- The remote consensus signer path exists and fails closed
  (`deploy/akash/REMOTE_SIGNER.md`). That is the mechanism a real validator key
  needs; it just is not turned on yet.
- Governance params are already set to Cosmos conventions with the reasoning
  recorded, and the `max_deposit_period + voting_period = unbonding_period`
  invariant in `config.yml` is the kind of thing chains get wrong.
- `community_tax: 0` and the inert `x/mint` params are deliberate and documented,
  so genesis reads honestly instead of reporting inflation the chain does not do.

## Explicitly not blocking

- The IBC relayer shipping in the same image and staying inert until the SDL
  enables it.
- `x/dex` simulation coverage *beyond* the ops listed above.
- The `ignite chain serve` developer flow, which should keep working via
  `DEV_INIT=1` and `config.yml`.
