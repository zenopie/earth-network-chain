# Container deployment — single-validator earth node

The image and the entrypoint here are the deployment. `docker-compose.yaml` runs
them on a plain Docker host or on SecretVM; `deploy/akash/` runs the same image
on Akash and shares this entrypoint. Anything below that is not about compose
specifically applies to both.

Mirrors `earth-network-backend`: pushing a `v*.*.*` tag has CI build the image,
push it to `ghcr.io/zenopie/earth-network-chain` and rewrite
`docker-compose.yaml` with the pinned digest. Tags only — a plain push
to `master` builds nothing.

**`docker-compose.yaml` is generated**, so edit the heredoc in the
workflow rather than the file — anything written directly into it is overwritten
by the next build.

The same release step also pins `deploy/akash/deploy.yaml`, but that file is
hand-maintained rather than generated: only its `image:` line is rewritten, and
everything else you put there survives.

    Dockerfile                          builds earthd on a slim runtime
    deploy/docker/entrypoint.sh         first-boot genesis, then earthd start
    docker-compose.yaml                 the deployed unit
    .github/workflows/docker-build.yml  builds and pins the digest

## Ports

    1317   LCD    the wallet apps and the ads-for-gas backend
    26656  p2p    other nodes
    26657  RPC    the explorer's block-range queries

All three are published with explicit host mappings so the addresses are
predictable — `EARTH_NODE_URL` and the apps need to point somewhere fixed.

p2p needs one thing beyond the mapping: set `EXTERNAL_ADDRESS` to the address
peers should dial. Without it CometBFT advertises the address it sees on itself,
which in a container is a private one, and hands that to every peer through PEX
— the node dials out fine and can never be dialled back. `SEEDS` and
`PERSISTENT_PEERS` give it somewhere to start; all three are written into
`config.toml` on every start, so a restart is enough to change one.

gRPC (9090) stays unpublished: everything here speaks REST.

## The volume is not optional

`earth-data:/data` holds genesis, the validator's consensus key and all chain
state. Without it a redeploy does not restart the chain — it creates a *different*
one, with a new genesis and new keys, and every address the apps knew about stops
existing. The entrypoint decides which case it is purely by whether
`/data/config/genesis.json` is there.

## Genesis

`deploy/genesis.json` is a build artifact, written by `scripts/build-genesis.sh`
from the sources in `deploy/genesis/` and committed alongside its sha256. See
`deploy/genesis/README.md`. It used to be `ignite chain init` followed by
hand-stripping and a manual "recompute bank supply", which is how `config.yml`
and the genesis file came to disagree about the pre-mine for two days without
anyone noticing.

It carries the 539 CSCAs, the seven register verifying keys, the ANML/ERTH pool,
the liquidity auction, the retirement schedules and the governance parameters —
and no validator set, because a gentx is bound to a consensus key and shipping
that key in a public image would let anyone sign as the validator.

## Three boot paths

The entrypoint picks one and says which. The difference between the first two is
the difference between joining a network and creating one.

| condition | what happens |
| --- | --- |
| `/data/config/genesis.json` exists | **resume** — start on the chain already in the volume |
| otherwise (default) | **join** — install `/etc/earth/genesis.json`, verify it against `/etc/earth/genesis.json.sha256`, start. No key created, no timestamp rewritten |
| `DEV_INIT=1` | **devnet** — generate a validator, stamp `genesis_time` to now, collect a gentx. A *new chain* every time |

The join path is the default because the old behaviour had no way to turn it off:
every node stamped its own `genesis_time` and minted its own validator, so two
containers from the same image could never share a chain. A hash mismatch is
fatal — a genesis swapped into the image after the fact fails loudly instead of
quietly forking whoever runs it.

`DEV_INIT=1` is the old behaviour, unchanged, and it is what you want for a
throwaway devnet. It is deliberately not set in the SDL or the compose file. It
also rewrites the genesis, so a devnet's genesis can never be mistaken for the
release: the hash no longer matches.

## Browser access

The two surfaces behave differently and only one can be scoped.

| | port | setting | granularity |
| --- | --- | --- | --- |
| RPC | 26657 | `RPC_CORS_ORIGINS=https://a,https://b` | an allowlist |
| LCD | 1317 | `API_UNSAFE_CORS=1` | `*` or nothing — all the SDK offers |

**`RPC_CORS_ORIGINS` closes a gap that has always been open.** CometBFT ships
`cors_allowed_origins = []`, so a browser could never reach the RPC
cross-origin — confirmed against the live devnet, which returns no CORS headers
there from the node or from Cloudflare. CosmJS talks to the RPC, so anything the
page does itself was blocked. Keplr masks it: signing is in the extension and
`keplr.sendTx` broadcasts from its background context, neither subject to page
CORS.

It is re-applied on every start, so an origin can be added or removed with a
restart rather than a volume wipe.

Two flags are off unless asked for:

- **`API_UNSAFE_CORS=1`** — any origin may read the LCD *and broadcast through
  it*. Fine on a public read-only node, wrong on a block producer.
- **`--keyring-backend test`** only appears on the `DEV_INIT` path now. The join
  path creates no keys at all, and a real validator's consensus key belongs
  behind `PRIV_VALIDATOR_LADDR`.

Run `deploy/docker/entrypoint_test.sh` to exercise all of it without building a
container.

Two accounts are seeded with 100k ERTH each so a fresh deployment is testable
without hunting for the validator's mnemonic: the development handset, and the
ads-for-gas hot wallet. Both are devnet keys with no value; drop them from
`deploy/genesis.json` for anything real.

`genesis_time` is stamped to the current time by the entrypoint before the
validator is created. The committed file carries the timestamp of the machine
that generated it, and CometBFT gives block 1 exactly that time while block 2
gets the wall clock — so the emission, prorated against elapsed time, would pay
the whole gap out in a single block. Left alone, a genesis committed a day
earlier minted 125,485 ERTH at height 2, and the lump grew for as long as the
file sat unchanged.

Regenerate it after any change to `config.yml`:

    ignite chain init --home /tmp/gen --skip-proto
    # then strip gen_txs, the dev accounts, and recompute bank supply

An earlier version ran `ignite chain init` inside the container instead. It
cannot work: init removes and recreates the home directory, and `/data` is a
mount point, so it fails with `Unlinkat //data: device or resource busy`.

## After the first boot

Dev accounts live in the test keyring on the volume:

    earthd keys list --keyring-backend test --home /data
    earthd keys export alice --keyring-backend test --home /data

Fund the ads-for-gas hot wallet from `alice`, and point the backend at this
node's LCD:

    EARTH_NODE_URL=rest+https://<host>:1317

## Devnet posture

`--api.enabled-unsafe-cors` is on so the web app can read the LCD straight from a
browser, and `--minimum-gas-prices 0uerth` accepts zero-fee transactions. Both
are fine for a throwaway chain and both want revisiting before anything real
runs on it.
