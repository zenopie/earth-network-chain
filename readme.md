# earth
**earth** is an application-specific blockchain built with the Cosmos SDK (v0.53) and
scaffolded with [Ignite CLI](https://ignite.com/cli).

- **ERTH** (`uerth`, micro-units: 1 ERTH = 1,000,000 uerth) — the staking coin and the hub
  asset of the built-in AMM (`x/dex`).
- **ANML** (`uanml`) — a proof-of-personhood token: one ANML minted per registered human
  per day, continuously bought back and burned with ERTH.

## Tokenomics — 4 ERTH/sec across two pillars

ERTH is emitted at a **fixed 4 ERTH/sec** (prorated by block time), as four independent
**1 ERTH/sec** streams grouped into two 2-ERTH/sec pillars:

| Pillar | Stream (1 ERTH/sec each) | Directed by | Where |
| --- | --- | --- | --- |
| **Investor** (`x/dex` + `x/deflation`) | Base staking → stakers | validators/delegators | `app/mint.go` |
| | Stake-weighted allocation | **plutocratic** vote (bonded stake) | `x/deflation/keeper/allocation.go` |
| **Democratic** (`x/caretaker`) | ANML buyback-and-burn | — (protocol) | `x/caretaker/keeper/abci.go` |
| | Democratic allocation | **one-human-one-vote** (registered humans) | `x/caretaker/keeper/allocation.go` |

The two allocation streams don't mint continuously — they accrue via a reward index and
are minted when realized (LP auto-compound, an option claim, or a registration payout), so
ERTH supply grows as rewards are actually distributed. Every dex swap also **burns** ERTH
(half the swap fee), and the buyback **burns ANML**.

## Get started

```
ignite chain serve
```

`serve` command installs dependencies, builds, initializes, and starts your blockchain in development.

> **Toolchain note:** Ignite runs Go with `GOTOOLCHAIN=local+path` while some of its
> proto/codegen tools (e.g. `buf`) require a newer Go than the one on `PATH`. If you
> hit `toolchain upgrade needed ... GOTOOLCHAIN=local+path`, put a Go ≥ 1.25.10 binary
> earlier on `PATH` before running `ignite`. A shim was set up for this repo at
> `~/.local/go-shim` (symlinks to a cached toolchain), so run Ignite/Go as:
> ```
> PATH="$HOME/.local/go-shim:$PATH" ignite chain serve
> ```

## The `x/dex` AMM module

A constant-product (`x·y=k`) **spoke-and-wheel** AMM. Every pool pairs the hub asset
**ERTH** (the staking/bond denom, resolved via the staking keeper) with a single spoke
token — so there is at most one pool per token, and a `tokenA → tokenB` swap is routed
through ERTH as two hops (`tokenA → ERTH → tokenB`).

- Pool reserves are held in the `dex` module account.
- LP positions are native bank coins (`dexlp/{poolId}`) minted/burned by the module, so
  total LP supply is tracked by `x/bank`.

**Fee model.** The swap fee is a governance parameter (`swap_fee`, updatable via
`MsgUpdateParams` from the gov account). It is a **percentage** (points of percent, e.g.
`0.3` means 0.3%) applied proportionally to the trade — not a flat amount. The fee is
always denominated in ERTH; on every hop it is split **half to the pool** (accrues to
LPs) and **half burned** (ERTH supply is permanently reduced).

**Parameters** (`earthd q dex params`)
- `swap_fee` — swap fee as a percent (default `0.3` = 0.3%).

**Messages / CLI** (`earthd tx dex ...`)
| Command | Effect |
| --- | --- |
| `create-pool [erth-amount] [token-amount]` | Create an ERTH↔token pool (one side must be ERTH); mints `sqrt(erth*token)` LP shares. Amounts may be given in either order. |
| `add-liquidity [pool-id] [amount-a] [amount-b]` | Deposit ERTH + token in the pool ratio; mints proportional LP shares (excess is not pulled). |
| `remove-liquidity [pool-id] [shares]` | Burn LP shares; returns a proportional share of both reserves. |
| `swap [token-in] [denom-out] [min-amount-out]` | Swap routed through the ERTH hub (1 or 2 hops), with per-hop fee/burn and a slippage guard. |

**Queries**: `earthd q dex list-pool`, `earthd q dex get-pool [id]`, `earthd q dex params`.

Example against a running dev chain (accounts `alice`/`bob` are pre-funded in `config.yml`):
```
# two spokes on the wheel
earthd tx dex create-pool 1000000uerth 1000000uusdc --from alice --keyring-backend test --chain-id earth --gas auto --gas-adjustment 1.5 -y
earthd tx dex create-pool 1000000uerth 500000uatom  --from alice --keyring-backend test --chain-id earth --gas auto --gas-adjustment 1.5 -y
# token -> token, routed uusdc -> ERTH -> uatom (burns ERTH on both hops)
earthd tx dex swap 100000uusdc uatom 40000 --from alice --keyring-backend test --chain-id earth --gas auto --gas-adjustment 1.5 -y
earthd q dex list-pool
```

## Investor pillar — stake-weighted allocations (plutocratic, 1 ERTH/sec)

The investor pillar's second stream (alongside base staking) is directed by **stakers'
votes**, weighted by their bonded stake. This stream lives in **`x/deflation`** (extracted
from `x/dex`). Stakers set percentages (summing to 100) across *allocation options*; each
option accrues ERTH pro-rata to the stake pointed at it, tracked with a reward index
(`x/deflation/keeper/allocation.go`). Vote weights are kept in sync with live bonded stake
via staking hooks (`x/deflation/keeper/hooks.go`) — delegating/undelegating re-weights your
vote automatically, no re-vote needed.

There are two kinds of allocation option, differing in how they deliver their ERTH:

- **`ALLOCATION_KIND_INTEGRATED`** — resolved automatically every block by a protocol
  handler named in the option's `handler` field. **Governance-permissioned to add**, since
  each handler is code that ships with the chain; unknown handler names are rejected at
  add-time. Integrated options are tracked in a dedicated key set, so `BeginBlocker` only
  ever iterates this bounded set.
  - **Option #1 (`lp_rewards`, seeded at genesis)** — "volume-weighted LP rewards". Its
    ERTH is split across dex pools by each pool's decaying trading volume (ERTH-denominated,
    ~7-day window) and **auto-compounded into each pool's `reserve_erth`**, raising every
    LP's redemption value pro-rata. Zero-volume pools get nothing.
- **`ALLOCATION_KIND_ADDRESS`** — accrues ERTH claimable by a fixed `recipient` via
  `claim-allocation`. **Permissionless to add**: any account may add one by burning
  `params.address_option_fee` ERTH (default 1 ERTH) as anti-spam. These settle *lazily* on
  claim rather than per-block, so permissionless additions cost no per-block work. An
  optional `--claimer` restricts who may trigger the claim; leave it empty (the default) and
  anyone can trigger it. The payout always goes to `recipient` either way — a triggerer only
  spends the gas.

**Messages / CLI**
| Command | Effect |
| --- | --- |
| `earthd tx deflation set-allocations --percentages '{"option_id":"1","percent":"100"}' [...]` | Set your stake-weighted split (must sum to 100). |
| `earthd tx deflation claim-allocation [option-id]` | Pay an ADDRESS option's accrued ERTH to its recipient. |
| `earthd tx deflation add-address-option [recipient] [description] [--claimer addr]` | Permissionless: add an ADDRESS option (burns the fee). |
| `earthd tx deflation add-integrated-option` | Governance-gated (authority = x/gov): add an INTEGRATED option. |

**Queries**: `earthd q deflation allocation-options`, `earthd q deflation allocation-option [id]`,
`earthd q deflation voter [address]`.

```
# vote 100% of your stake weight to volume-weighted LP rewards
earthd tx deflation set-allocations --percentages '{"option_id":"1","percent":"100"}' \
  --from alice --keyring-backend test --chain-id earth --gas auto --gas-adjustment 1.5 -y
earthd q deflation allocation-option 1   # amount_allocated tracks your bonded stake
```

## Democratic pillar — `x/caretaker` (proof-of-personhood, 2 ERTH/sec)

The democratic pillar is gated on **proof-of-personhood registration**. Instead of a
trusted backend, the app scans a passport and generates a **zk proof** on-device; the
chain verifies a **Barretenberg UltraHonk** proof (`zk/ultrahonk`) against the
governance-set verifying key selected by `signature_algorithm`
(`params.verifying_keys`), pins `current_date` to block time, binds the proof to the
live DSC-registry root (`x/pki`), and dedups on the nullifier. The passport register
circuits (`lean_poa` + per-DSC-algorithm variants) live in `earth-network-mobile/circuits`;
the DSC registry and its CSCA trust anchor are `x/pki` — see
[`docs/DSC_REGISTRY_OPTION_C.md`](docs/DSC_REGISTRY_OPTION_C.md).

- **ANML token** (`uanml`, 1 ANML = 1e6 uanml) — minted 1/day per registered human.
- **Buyback-and-burn (1 ERTH/sec)** — `BeginBlock` mints ERTH, swaps it for ANML on the
  dex (`dexKeeper.SwapExactIn`), and burns the ANML (deflationary for ANML).
- **Democratic allocations (1 ERTH/sec)** — same reward-index mechanism as the investor
  pillar but **one-human-one-vote** (each registered voter has equal weight), gated on a
  valid registration. The same two option kinds apply: `DEMOCRATIC_KIND_INTEGRATED`
  (gov-permissioned, handler-resolved) and `DEMOCRATIC_KIND_ADDRESS` (permissionless behind
  a burned `address_option_fee`, claimed to a fixed recipient, with the same optional
  `--claimer` gate). **Option #1 = registration rewards** (integrated,
  `registration_rewards`): its accrued ERTH is paid out on each new registration,
  **50% registree / 50% referrer**.

**Messages / CLI** (`earthd tx caretaker ...`): `register --proof <b64> --public-signals <s,s,…> --signature-algorithm <id> [--affiliate <addr>]`,
`claim-anml`, `set-democratic-allocations --percentages ...`, `claim-democratic-allocation [id]`,
permissionless `add-address-option [recipient] [description] [--claimer addr]`, and gov-gated
`add-integrated-option`. **Queries**: `caretaker registration [addr]`,
`caretaker democratic-options`, `caretaker democratic-voter [addr]`.

The registration nullifier is derived deterministically in-circuit from the passport
(name + date of birth), so a renewed passport yields the same nullifier (one person, one
registration) and the issuing state gets no extra exposure. See
[`docs/DSC_REGISTRY_OPTION_C.md`](docs/DSC_REGISTRY_OPTION_C.md) for the DSC-registry design.

### Configure

Your blockchain in development can be configured with `config.yml`. To learn more, see the [Ignite CLI docs](https://docs.ignite.com).

### Web Frontend

Additionally, Ignite CLI offers a frontend scaffolding feature (based on Vue) to help you quickly build a web frontend for your blockchain:

Use: `ignite scaffold vue`
This command can be run within your scaffolded blockchain project.


For more information see the [monorepo for Ignite front-end development](https://github.com/ignite/web).

## Release
To release a new version of your blockchain, create and push a new tag with `v` prefix. A new draft release with the configured targets will be created.

```
git tag v0.1
git push origin v0.1
```

After a draft release is created, make your final changes from the release page and publish it.

### Install
To install the latest version of your blockchain node's binary, execute the following command on your machine:

```
curl https://get.ignite.com/earth-network/earth@latest! | sudo bash
```
`earth-network/earth` should match the `username` and `repo_name` of the Github repository to which the source code was pushed. Learn more about [the install process](https://github.com/ignite/installer).

## Learn more

- [Ignite CLI](https://ignite.com/cli)
- [Tutorials](https://docs.ignite.com/guide)
- [Ignite CLI docs](https://docs.ignite.com)
- [Cosmos SDK docs](https://docs.cosmos.network)
- [Developer Chat](https://discord.com/invite/ignitecli)
