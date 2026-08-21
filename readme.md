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
| **Investor** | Base staking → stakers | validators/delegators (standard `x/distribution`) | `x/earth` |
| | Capital allocation stream | **plutocratic** vote (bonded stake) | `x/allocation` |
| **Democratic** | ANML buyback-and-burn | — (protocol) | `x/personhood/keeper/abci.go` |
| | Human allocation stream | **one-human-one-vote** (registered humans) | `x/allocation` |

The **base staking** stream is the only part of issuance that touches the SDK's own
reward machinery, and it uses it unmodified: `x/earth` mints its 1 ERTH/sec into the fee
collector during `BeginBlock` and `x/distribution` takes it from there — split by voting
power, validator commission withheld at each validator's configured rate, and claimed with
the usual `MsgWithdrawDelegatorReward` / `MsgWithdrawValidatorCommission`. The only thing
this chain changes is *how much* is minted (a fixed per-second rate instead of
bonded-ratio inflation), never who may claim it or on what terms.

**`community_tax` is 0.** The SDK default skims 2% of staking rewards into a pool
governance then votes to spend; the two allocation streams already do that job, with their
own dedicated 1 ERTH/sec each and a continuous vote rather than a proposal. So the base
staking stream reaches stakers whole, and the emission table above is the complete answer
to where ERTH goes.

Both allocation streams are one engine in **`x/allocation`**, keyed by a stream id
(`human` / `capital`). They share the options, the reward-index maths, the epoch reset
and the claim path; they differ only in where a voter's weight comes from and who is
allowed to vote at all.

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

**Protocol-owned liquidity, and its ten-year exit.** The chain starts owning all of
its own liquidity: the genesis ANML/ERTH pool's LP shares are minted to the `dex`
module account, and the liquidity auction's pool mints its shares there too when it
settles. That account has no key, so nothing — governance included — can withdraw
those positions. They are not permanent, though. Each is retired on a straight line
over **ten years**, because running a book is active management whose incentives are
not an ERTH staker's, and a position nobody can adjust is the worst version of that
mismatch. Retiring it makes room for providers who will actually manage the
liquidity, and gives them a decade of steadily rising reward share as the reason to
show up.

Retirement is not a withdrawal: a slice of the position is priced against the
reserves exactly as a real withdrawal would be, and then destroyed.

- **ANML/ERTH** burns *both* sides. Both assets are the chain's own, so both are
  destroyed; both reserves shrink by the same fraction, which means retirement never
  moves the pool's price.
- **The auction pool** burns only the **ERTH**. Its spoke side is a bridged asset the
  chain cannot recreate, so it is left in the reserve. That walks the pool's price up
  on purpose: arbitrageurs sell ERTH in and take the spoke asset out, so the auction's
  proceeds end up buying ERTH off the market — and the ERTH they buy is burned by the
  tranches that follow. The protocol never gets a treasury it could spend, because a
  protocol swap against its own pool cannot move the spoke asset out; only an outside
  buyer can.

The target is recomputed from the block clock each block rather than accumulated, so a
chain halt cannot push the end date out and truncation never compounds. Progress is
visible at `earthd q dex pol-burns`; a finished schedule is deleted, so an empty list
means the protocol no longer owns liquidity it is still retiring.

**The module checks its own books every block.** The dex module account holds the
whole pre-mine in one balance, and six paths move it — swap fee burns, LP
unbonding payouts, LP reward minting, deposits, the ANML buyback, and POL
retirement. The EndBlocker asserts that what the module's records say it owes and
what it actually holds are **exactly** equal, in both directions: a shortfall is
a withdrawal that will not be payable, and a surplus is coins that should have
been destroyed and were not — which nothing else would ever notice. A breach
halts the node rather than letting the chain keep trading on mispriced reserves.
The chain's own module accounts are therefore blocked from receiving ordinary
transfers, since an outside deposit would otherwise be indistinguishable from a
bug.

**Parameters** (`earthd q dex params`)
- `swap_fee` — swap fee as a percent (default `0.3` = 0.3%).

**Messages / CLI** (`earthd tx dex ...`)
| Command | Effect |
| --- | --- |
| `create-pool [erth-amount] [token-amount]` | Create an ERTH↔token pool (one side must be ERTH); mints `sqrt(erth*token)` LP shares. Amounts may be given in either order. |
| `add-liquidity [pool-id] [amount-a] [amount-b]` | Deposit ERTH + token in the pool ratio; mints proportional LP shares (excess is not pulled). |
| `remove-liquidity [pool-id] [shares]` | Burn LP shares; returns a proportional share of both reserves. |
| `swap [token-in] [denom-out] [min-amount-out]` | Swap routed through the ERTH hub (1 or 2 hops), with per-hop fee/burn and a slippage guard. |

**Queries**: `earthd q dex list-pool`, `earthd q dex get-pool [id]`, `earthd q dex params`,
`earthd q dex pol-burns`.

Example against a running dev chain (accounts `alice`/`bob` are pre-funded in `config.yml`):
```
# two spokes on the wheel
earthd tx dex create-pool 1000000uerth 1000000uusdc --from alice --keyring-backend test --chain-id earth-1 --gas auto --gas-adjustment 1.5 -y
earthd tx dex create-pool 1000000uerth 500000uatom  --from alice --keyring-backend test --chain-id earth-1 --gas auto --gas-adjustment 1.5 -y
# token -> token, routed uusdc -> ERTH -> uatom (burns ERTH on both hops)
earthd tx dex swap 100000uusdc uatom 40000 --from alice --keyring-backend test --chain-id earth-1 --gas auto --gas-adjustment 1.5 -y
earthd q dex list-pool
```

## Allocation streams — `x/allocation` (2 ERTH/sec)

Two of the four streams are directed by *votes* rather than by protocol rules, and both
run the same engine in **`x/allocation`**:

| Stream | Who may vote | Weight |
| --- | --- | --- |
| `human` | anyone with a live proof-of-personhood registration | flat, identical for every human |
| `capital` | anyone with bonded stake | their bonded stake |

Voters set percentages (summing to 100) across that stream's *allocation options*; each
option accrues ERTH pro-rata to the weight pointed at it, tracked with a reward index
(`x/allocation/keeper/allocation.go`). All state is keyed by stream first, so option ids,
totals and epochs are per stream — the human stream's option #1 and the capital stream's
option #1 are two different options, and a governance reset of one slate leaves the other
standing.

Capital-stream weights are kept in sync with live bonded stake via staking hooks
(`x/allocation/keeper/hooks.go`) — delegating/undelegating re-weights your vote
automatically, no re-vote needed. Human-stream weights are cleared when a registration
lapses, by `x/personhood`'s expiry sweep.

There are two kinds of allocation option, differing in how they deliver their ERTH:

- **`ALLOCATION_KIND_INTEGRATED`** — resolved automatically every block by a protocol
  handler named in the option's `handler` field. **Governance-permissioned to add**, since
  each handler is code that ships with the chain; unknown handler names, and handlers
  belonging to the other stream, are rejected at add-time. Integrated options are tracked
  in a dedicated key set, so `BeginBlocker` only ever iterates this bounded set.
  - **capital option #1 (`lp_rewards`, seeded at genesis)** — "volume-weighted LP rewards".
    Its ERTH is split across dex pools by each pool's decaying trading volume
    (ERTH-denominated, ~7-day window) and **auto-compounded into each pool's
    `reserve_erth`**, raising every LP's redemption value pro-rata. Zero-volume pools get
    nothing. The handler lives in `x/dex`, which registers it with `x/allocation`.
  - **human option #1 (`registration_rewards`, seeded at genesis)** — resolves nothing per
    block; the pool stacks and is drawn down on each new registration, **50% registree /
    50% referrer**. Registered by `x/personhood`.
- **`ALLOCATION_KIND_ADDRESS`** — accrues ERTH claimable by a fixed `recipient` via
  `claim-allocation`. **Permissionless to add** in either stream: any account may add one
  by burning `params.address_option_fee` ERTH (default 1 ERTH) as anti-spam. These settle
  *lazily* on claim rather than per-block, so permissionless additions cost no per-block
  work. An optional `--claimer` restricts who may trigger the claim; leave it empty (the
  default) and anyone can trigger it. The payout always goes to `recipient` either way — a
  triggerer only spends the gas.

**Messages / CLI** — every command names the stream (`human` or `capital`):

| Command | Effect |
| --- | --- |
| `earthd tx allocation set-allocations [stream] --percentages '{"option_id":2,"percent":100}'` | Set your split in that stream (must sum to 100; empty clears it). |
| `earthd tx allocation claim-allocation [stream] [option-id]` | Pay an ADDRESS option's accrued ERTH to its recipient. |
| `earthd tx allocation add-address-option [stream] [recipient] [description] [--claimer addr]` | Permissionless: add an ADDRESS option (burns the fee). |
| `earthd tx allocation add-integrated-option` | Governance-gated (authority = x/gov): add an INTEGRATED option. |
| `earthd tx allocation reset-allocations` | Governance-gated: retire one stream's whole slate of votes. |

**Queries**: `earthd q allocation options [stream]`, `earthd q allocation option [stream] [id]`,
`earthd q allocation voter [stream] [address]`, `earthd q allocation params`.

```
# vote 100% of your stake weight to volume-weighted LP rewards
earthd tx allocation set-allocations capital --percentages '{"option_id":1,"percent":100}' \
  --from alice --keyring-backend test --chain-id earth-1 --gas auto --gas-adjustment 1.5 -y
earthd q allocation option capital 1   # amount_allocated tracks your bonded stake
```

## Democratic pillar — `x/personhood` (proof-of-personhood)

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
- **The human allocation stream (1 ERTH/sec)** lives in `x/allocation`; this module only
  supplies its weight source (one live registration = one vote), clears a lapsed human's
  vote, and draws down the registration-reward pool.

**Messages / CLI** (`earthd tx personhood ...`): `register --proof <b64> --public-signals <s,s,…> --signature-algorithm <id> [--affiliate <addr>]`,
`claim-anml`. **Queries**: `personhood registration [addr]`, `personhood registration-count`,
`personhood params`.

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
