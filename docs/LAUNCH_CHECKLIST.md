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

- [ ] **The validator set at height 1.** Decided: launch single-validator, and
      say so in public along with the plan for opening it up. The validator
      account is in genesis — `earth14e6sqtf5y7mtzwykqreewe9kg3w94t0f25d54a`,
      1,000 ERTH, enough to self-delegate 100 and fund governance deposits for
      years. No pre-allocation for anyone else: a would-be validator earns ERTH
      by registering a passport or by bidding in the liquidity auction, which is
      a better property than a founder allocation list.
      **Still open:** the gentx itself, which needs the key and so happens on
      launch day. And saying it in public.

      Note there is no "plan for opening it up" to write, because nothing is
      closed: `max_validators` is 100, there is no allowlist, and a second
      validator needs only ERTH and `MsgCreateValidator`. The plan is the
      absence of one, which is already on docs.erth.network. What is worth
      saying at launch is the current fact — one validator — not a roadmap to
      permissionlessness that already exists.

      Worth stating plainly when you do: a single validator can censor
      registrations, which is the one thing the chain exists to make
      unforgeable. It also collects the entire staking pillar — 1 ERTH/sec, or
      86,400 ERTH a day — for as long as it is alone. Neither is a bug; both
      read badly if discovered rather than disclosed.
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
- [x] **A nonzero minimum gas price.** `0.005uerth`, in the entrypoint default
      and the SDL — ~500uerth on a typical transaction. Per-node config, not a
      chain parameter, so it is a restart to change. Also set a block gas limit:
      `max_gas` was `-1`, meaning a block was bounded only by its 22MB size and
      the fee was the only brake on one transaction eating all of it. Now 100M.
- [x] **Who holds the gov keys at launch.** Nobody: the authority for all of
      those is the gov module account, which has no private key. The real
      question is who has the voting power to pass a proposal, and at launch that
      is the sole validator — not by holding anything special, but by being the
      only staker. It resolves itself as others stake.
      What did need deciding was timing, and that is
      `docs/TRUST_STORE_RUNBOOK.md`: a compromised Document Signer goes on the
      expedited track, one day rather than seven, with the proposal drafted in
      advance.

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
- [x] **Set `consensus.version.app`.** Now `1`, in
      `deploy/genesis/chain.json`.
- [x] **Drop the devnet accounts.** The ads-for-gas hot wallet is out of genesis.
      Its key was a devnet key that had been on a laptop, and a launch genesis is
      not the place to fund an operational wallet from one.
      **Consequence to handle after launch:** ads-for-gas has no funds at height
      1. Fund it from the validator, with a key that has never left a signer.
      Only the validator and the dex module now hold anything.
- [x] **Publish the sha256 of the final genesis** ~~in the release notes, and
      have the entrypoint *verify* it rather than generate anything.~~ Done for
      the entrypoint: join is the default, a hash mismatch against
      `/etc/earth/genesis.json.sha256` is fatal, and the old self-init sits
      behind `DEV_INIT=1`. `deploy/docker/entrypoint_test.sh` covers all three
      paths — 16 assertions, verified by mutating the entrypoint three ways.
      **Still open:** putting the hash in the release notes, which needs a
      release to exist.
- [x] **Stop using `--keyring-backend test` on the init path.** The join path
      creates no keys at all, so the test keyring — and `VALIDATOR_MNEMONIC` —
      now exist only under `DEV_INIT=1`, where the chain is disposable by
      construction. A real validator's consensus key goes behind
      `PRIV_VALIDATOR_LADDR`.
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

- [x] **Expose p2p.** The `Dockerfile` already exposed 26656 — the earlier
      version of this entry said otherwise and was wrong. What was actually
      missing was everything around it: `docker-compose.yaml` published only
      1317 and 26657, the Akash SDL deliberately exposed no p2p port at all, and
      the entrypoint set no `external_address`, so a containerised node
      advertised its pod address to every peer it met and could never be dialled
      back.
      Now: compose publishes 26656; the SDL exposes it `global: true` on a
      provider port rather than through the tunnel, since peers speak raw TCP
      and cloudflared serves HTTP; and the entrypoint takes `EXTERNAL_ADDRESS`,
      `SEEDS` and `PERSISTENT_PEERS`, applied on every start, saying so in the
      log when the external address is unset.
      **Still to do on Akash:** adding the port is a structural SDL change, so
      it needs a lease replacement, and closing the lease destroys the volume —
      genesis, the consensus key, every account. Do it as part of the genesis
      cutover while there is no state worth keeping. The provider assigns the
      external port, so read it from the lease and set `EXTERNAL_ADDRESS` to the
      provider's host and *that* port.
- [ ] **Publish seeds.** At least one seed node's `nodeid@host:26656`, in the
      README and in the release notes.
- [x] **Turn off `--api.enabled-unsafe-cors`** for validator nodes. Now
      `API_UNSAFE_CORS`, off by default and logging a warning when on.

      Measured against the live devnet rather than assumed, which changed the
      answer twice. The LCD returns `access-control-allow-origin: *` to any
      origin, from the node's own flag. The RPC returns **no CORS headers at
      all** — not from the node (`cors_allowed_origins = []`) and not from
      Cloudflare, which adds none anywhere. So a browser has never been able to
      reach the RPC cross-origin on any deployment of this chain, and the web
      app's page-level traffic must be going to the LCD.

      Keplr hides part of that: signing is in the extension and `keplr.sendTx`
      broadcasts from its background context, neither subject to page CORS.
      Anything the page does itself — a `StargateClient` query, a
      `SigningStargateClient` broadcast — is.

      New `RPC_CORS_ORIGINS` takes a comma-separated allowlist and writes
      `cors_allowed_origins` into `config.toml`, re-applied on every start so an
      origin can change without wiping the volume. The SDL sets it to
      `https://erth.network`. The LCD stays all-or-nothing because that is all
      the SDK offers — `server/config` has a bool and no allowlist.
- [ ] **Split the browser-facing LCD off the block producer.** One node is
      currently both, so `API_UNSAFE_CORS=1` (literally `*`) sits on a validator.
      Two ways out and they compose: run a separate read-only LCD/RPC service, and
      scope the header at Cloudflare, which already terminates both hostnames and
      can rewrite it per origin.
- [x] **Set snapshot and pruning defaults.** `SNAPSHOT_INTERVAL` (1000, ~80
      minutes at 5s) and `SNAPSHOT_KEEP_RECENT` (5) in the entrypoint, applied on
      every start so the cadence can change with a restart. The SDK default is 0
      — off — which means no node offers snapshots and nobody can state-sync;
      since a snapshot cannot be produced for a height already passed, launching
      that way is a decision that cannot be revisited.
      Verified on a live chain: five snapshots on disk with `creating` and
      `completed` in the log.
      Pruning profiles per node role and the client-side state-sync procedure are
      in `docs/JOIN.md`. This chain gains more from state sync than most —
      replay re-executes every registration's zkSNARK verification, so sync cost
      grows with adoption, not just with time.
- [x] **Write the join procedure** — `docs/JOIN.md`, walked end to end against
      the real binary. Two values wait for launch day: the seed address and the
      genesis hash.
- [x] **Revisit the slashing window.** Both values were the SDK defaults, not
      choices: a 100-block window at 50% jails after 50 missed blocks, about four
      minutes at 5s. With one validator that means a container restart halts the
      chain and slashes 1% of its stake. Now 10,000 / 0.05, the Hub's setting,
      giving ~13 hours — and with a single validator the usual cost of leniency,
      a dead validator lingering in the set, does not apply.
      `historical_entries: 10000` turns out to be the SDK default exactly
      (`DefaultHistoricalEntries`), not 100x it. That note was wrong.

---

## 3. Binary release engineering

- [x] **Publish binaries at all.** `release.yml` now fires on a version tag and
      builds `earthd` natively on Linux amd64 and arm64 — natively because the
      Barretenberg verifier is cgo, so Ignite's cross-compile could never have
      worked. Publishes tarballs, a `checksums.txt` that also covers
      `deploy/genesis.json`, and the genesis file itself, with the genesis sha256
      in the release notes.
- [x] **Build the image with the version ldflags.** `VERSION` and `COMMIT` are
      build args passed from the tag and `github.sha`, and the build is
      `-trimpath`ed so the binary does not vary with the checkout path. The
      toolchain is pinned to `golang:1.25.10-trixie` rather than floating on
      `1.25`.
- [x] **Stop pushing `:latest`.** The image is built and pushed under the
      version tag only, and the digest is read back from that tag.
- [ ] **Reproducible builds.** cgo plus a prebuilt Aztec archive means two
      operators can plausibly end up with differently-behaving verifiers, which
      is a consensus fault, not a packaging annoyance. Add `-trimpath`, pin the
      toolchain, publish per-platform sha256, and make `verifier-libs.yml` the
      only source of `libbarretenberg.a` — verified against
      `third_party/barretenberg-go/checksums.json` at build time.
- [x] **Get compiled artifacts out of the tree.** `poafixtures` is gone — a
      built binary nothing referenced, committed by accident, regenerable with
      `go run ./tools/poafixtures`. `.gitignore` now covers it and stray
      `earthd` builds.
      **`lib/darwin_arm64/libbarretenberg.a` stays, deliberately.** It is the
      development platform, so removing it means a fresh clone cannot build until
      it fetches ~100MB from Aztec — and because the blob is already in history,
      removing it would not shrink a clone anyway (`.git` is 35MB total; object
      code packs well). What *is* now ignored is every other platform's library,
      so a local `build-wrapper.sh --platform linux_amd64` cannot add another
      50MB by accident.
- [x] **Add a `LICENSE`.** Apache 2.0, chosen over MIT for the patent grant and
      because the Cosmos SDK this is built on uses it.
- [x] **Add a `CHANGELOG.md`.** Written for operators rather than as a commit
      digest, with consensus-affecting changes called out separately — for a
      chain those are breaking whatever the diff looks like. The release workflow
      links it.

---

## 4. The upgrade path has to be exercised once

`app/upgrades.go` is well-built — named handlers, store loader, skip-height
handling — and `var Upgrades = []Upgrade{}` is empty, so none of it has ever run.
An untested upgrade path is discovered during the upgrade.

- [x] **Rehearse a no-op upgrade end to end.**
      `scripts/rehearse-upgrade.sh` does it on a throwaway chain: passes a real
      proposal, waits for the halt, checks the old binary *refuses* to continue,
      rebuilds with a matching handler, checks the chain resumes. All three
      behave.
      It found the mistake that costs a week: the plan height must be beyond the
      **end of the voting period**, not beyond now. A plan whose height has
      passed by the time the proposal executes is rejected, and the proposal
      reads `PROPOSAL_STATUS_FAILED` — passed the vote, failed to apply. With a
      7-day voting period that is ~120,000 blocks of margin. The first run of the
      script failed exactly this way.
- [x] **Cosmovisor: decided against.** Not bundled, deliberately. Upgrades are
      a binary swap at a halt height, and `docs/UPGRADES.md` is written from a
      rehearsal of exactly that. An operator who wants cosmovisor can run it
      around the released binary; nothing here prevents it.
- [x] **Document the halt-height procedure** — `docs/UPGRADES.md`, written from
      the rehearsal rather than from the SDK docs. Covers the plan-height
      arithmetic, `StoreUpgrades` when the module set changes,
      `--unsafe-skip-upgrades` and why it must be coordinated, and what a
      halted-again node means.
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
- [x] **`x/allocation`'s invariant walked every option, every block.** Fixed.
      `AssertInvariants` ran in the EndBlocker and `CheckStreamWeight` summed a
      stream by walking all of its options, decoding each record in full. Adding
      an ADDRESS option is permissionless, so that walk grew for a one-time fee:
      one ERTH burned, gas paid in the block that stored the row, and every node
      re-reading it in every block afterwards — the shape `x/dex` had before
      `TestPoolSetIsNoLongerUnbounded`.
      The sum is now maintained rather than recomputed. `setOption` is the only
      writer of an option record and moves a per-stream `summed_weight` by what
      each option's balance actually changed by; the EndBlocker compares that
      against `total_weight`. Two numbers per stream, whatever the option count:
      `TestInvariantCostIsFlatInOptionCount` measures 4,246 gas at five options
      and the same 4,246 at five hundred.
      It stays a real check rather than a number agreeing with itself because the
      two aggregates are maintained at different sites and from different
      quantities — `resyncVoter` moves the total by a voter's whole weight, and
      the sum moves per option and is never clamped. The clamped case is the one
      that proves it, and it still halts.
      What the bounded pair gives up is an option written around `setOption` with
      the total left alone: both aggregates miss it and so agree. That is a bug
      in the module rather than anything a user can cause, and the walk that
      catches it now lives in `AssertInvariants`, which the tests run after every
      operation and an operator can run against a halted node to learn which of
      the two numbers is wrong. Genesis validation walks the options on every
      import, and the running sum is rebuilt from them there.
- [x] **Permissionless options grew the state and the query with no way back.**
      Fixed, in the two places it showed. Adding an ADDRESS option is
      permissionless and its fee is paid once, so every option ever added was a
      row stored forever whether or not a single voter pointed at it, and the
      `Options` query returned all of them on a route that costs the caller
      nothing.
      Options now expire: thirty days carrying no weight and the option is
      removed, along with any rewards it earned and nobody claimed. Nothing is
      burned — an option's accrued ERTH is minted at claim, so a forfeited
      balance is issuance that never happens — and anyone may trigger a claim on
      an ADDRESS option, with the payout going to the recipient whoever sends it.
      Governance's INTEGRATED options are never swept. The schedule is a queue
      ordered by due date maintained on every option write, so a quiet block
      reads one key and a busy one removes at most 20; the cap is in the
      per-block budget in `app/block_budget_test.go`.
      The query is paged at 100. Walking the whole table is still possible, but
      page by page, as separate requests a node can meter.
- [x] **The public-input schema is pinned, and the values are right.** This
      entry used to say `verifyRegistrationProof` carried placeholder indices
      that could not be adjusted later without invalidating every registration.
      Both halves are out of date, and the entry was repeated as a launch
      blocker long after the work was done.
      The positions are governance parameters —  `params.NullifierIndex`,
      `params.DscKeyIndex`, `params.CurrentDateIndex`, seeded 1, 2 and 0 — and
      they match the circuits: `main.nr` takes one public input, `current_date`,
      and returns `(nullifier, dsc_key)`, giving `[current_date, nullifier,
      dsc_key]`. All seven signature variants share that public surface, so one
      set of indices serves every algorithm.
      Checked against a real proof rather than by reading the source:
      `x/personhood/keeper/testdata/lean_poa/public_inputs` holds exactly three
      field elements — `250101`, then `expected_nullifier`, then
      `expected_dsc_key` — and the handset splits three in the same order.
- [x] **The verifying keys are seeded, and `dsc_root` no longer exists.** Seven
      distinct keys ship in `deploy/genesis/verifying-keys/`, one per algorithm,
      and the `lean_poa` key is byte-identical to the one the keeper test
      verifies a real bb proof against — `bb write_vk` output from the real
      circuits, not demo keys. `dsc_root` is `reserved` in the params proto: a
      registration now carries the Document Signer certificate, checked against
      the 539 CSCAs genesis seeds from `csca/`.
- [x] **The Linux verifier libraries are built by CI, not checked in.**
      `third_party/barretenberg-go/lib/` holds `darwin_arm64` and nothing else,
      which reads like a gap and is not one. `release.yml` builds
      `libbarretenberg.a` on a native runner per architecture — no
      cross-compiling, since earthd links the verifier through cgo — before
      building `earthd`, checksum-verified against `checksums.json` at bb
      v5.0.0. `verifier-libs.yml` builds the same libs on release and attaches
      them. The Dockerfile builds it in-image. The checked-in macOS lib is a dev
      convenience; committing multi-megabyte archives for every platform is the
      thing being avoided.
- [ ] **Prove on real hardware, end to end.** The on-device path is written —
      `PassportProver` → `NoirProver` → bb v5.0.0 poseidon2 proof of `lean_poa`,
      split into the chain's `(proof, public_signals)` form — but no real
      passport has been scanned on a real handset into a real node. Keep
      noir_android's bb version in lockstep with the chain's v5.0.0, or
      large-circuit proofs are rejected on arrival.
- [x] **Write the trust-store runbook.** `docs/TRUST_STORE_RUNBOOK.md`.
      Revocation goes expedited. It also names two gaps rather than hiding them:
      there is no `MsgRevokeCsca`, so a country's root going bad has no fast
      path, and the same is true for a CSCA issuing certificates it should
      not.
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

- [x] `docs/JOIN.md` — written, and walked end to end against the real binary.
      Doing that found a step the page was missing: the node refuses to start
      until `minimum-gas-prices` is set, with an error that does not name the
      file. Three values stay `TBD` until launch — seed address, genesis hash,
      gas price.
- [ ] Release notes per tag: binary checksums, genesis sha256 (launch tag only),
      upgrade name and height (upgrade tags only).
- [x] **A documentation site.** `docs-site/` — Docusaurus, the same thing the
      Cosmos SDK and most Cosmos chains use, published to
      `docs.erth.network` by `.github/workflows/docs.yml`. Six pages covering
      what Earth is, registering, emission, using the app, governance and running
      a node. Every page has an edit link to its markdown, and the build fails on
      a broken link. Operational guides stay in `docs/` next to the code and are
      linked rather than copied, so they cannot drift within a release.
      **Two one-time setup steps remain, both outside this repo:** a `CNAME` DNS
      record for `docs` pointing at `zenopie.github.io` (Cloudflare SSL mode
      **Full**, not Flexible — Flexible loops against Pages), and GitHub
      Settings → Pages with source *GitHub Actions* and the custom domain set.
- [x] Replace the Ignite boilerplate in the readme. Gone: the `git tag v0.1`
      instructions for a workflow that no longer works that way, the
      `get.ignite.com` install line pointing at the wrong repo, and the Vue
      scaffolding section. Replaced with running a node, developing, releasing,
      and an index of the docs.

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
