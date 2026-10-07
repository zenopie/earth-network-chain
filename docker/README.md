# Container image — earth node

A generic image: `earthd`, its libraries, the release genesis, and a minimal
entrypoint. How a particular operator hosts a node (key handling, a
supervisor, a relayer, a request filter, its orchestration) is deployment, and
belongs in an image built `FROM` this one by digest.

Pushing a `v*.*.*` tag has CI build the image and push it to
`ghcr.io/zenopie/earth-network-chain`. Tags only — a plain push to `master`
builds nothing, and CI writes the digest nowhere. Pin it by digest.

    Dockerfile                          builds earthd on a slim runtime
    docker/entrypoint.sh                first start: the release genesis; then earthd start
    docker/entrypoint_test.sh           the entrypoint's tests, no container needed
    .github/workflows/docker-build.yml  builds and pushes the image

## What the entrypoint does

| `$EARTH_HOME/config/genesis.json` | what happens |
| --- | --- |
| missing (a fresh volume) | checks `/etc/earth/genesis.json` against `/etc/earth/genesis.json.sha256`, `earthd init`, installs it unmodified, starts |
| present | starts on what the volume holds (and warns if its genesis is not the image's) |

A hash mismatch is fatal: a genesis swapped into the image after the fact fails
loudly instead of quietly forking whoever runs it. No account or validator key
is created or imported; `earthd init` writes a random node key and consensus
key, which is what a non-validating node needs. `genesis_time` is never
rewritten: emission is prorated from it, so a rewritten one pays the gap out at
height 2.

It then runs `earthd start` with RPC and LCD listening on all interfaces
(`--rpc.laddr tcp://0.0.0.0:26657 --api.enable --api.address tcp://0.0.0.0:1317`),
followed by any arguments given to the container, which win.

The node runs as `earth` (uid 10001), never root: earthd runs native code on
input strangers choose (the proof verifier, the CosmWasm engine). `/data` is
owned by it in the image; a volume owned by someone else needs its ownership
set by whoever mounts it.

    docker run -v earth-data:/data -p 26656:26656 \
      -e EARTHD_MINIMUM_GAS_PRICES=0.005uerth \
      ghcr.io/zenopie/earth-network-chain@sha256:<digest>

## Configuration

earthd reads `app.toml` and `config.toml` under `$EARTH_HOME/config`, then
`EARTHD_*` environment variables (the key with `.` and `-` as `_`), then flags.
Environment and flags override the files, which live on the volume.

Required:

- **A minimum gas price** (`EARTHD_MINIMUM_GAS_PRICES`, or `minimum-gas-prices`
  in `app.toml`): the node refuses to start without one. uerth only: ANML exists
  only in the shielded pool and is refused as a fee.

What the chain itself fixes, whatever the configuration says:

- **The app mempool is the no-op one.** Private txs are unsigned, and the SDK's
  priority and sender-nonce mempools refuse any tx with no signer; `app.go`
  ignores `mempool.max-txs` (loudly) and always runs the no-op mempool.
- **Signed txs are simulated under `[wasm] simulation_gas_limit`**, 10M gas when
  unset (`app/ante.go`); private txs are simulated up to the block gas limit.

Worth setting on any node that serves RPC or LCD to others (none is consensus;
each is node-local):

| Setting | Why |
| --- | --- |
| `index-events` (app.toml; `EARTHD_INDEX_EVENTS`) | Empty indexes every event attribute, so every address becomes a `tx_search` key, and CometBFT loads every match of a search before it pages. Index only what your clients search by (an IBC relayer: the `*_packet.packet_*` keys). `tx.hash` and `tx.height` are always indexed. |
| `query-gas-limit` (app.toml) | 0 is unbounded: one query can walk a whole store. |
| `[rpc] max_subscription_clients`, `max_open_connections`; `[api] max-open-connections` | Each subscriber is also a `broadcast_tx_commit` in flight; size these to what the node serves. |
| `[storage] discard_abci_responses = false`, `[tx_index] indexer = "kv"`, `pruning = "nothing"` | Only on a node that serves history: an indexer reading `block_results` from height 1 needs every height's results kept. |
| `log_level` | At `debug`, the RPC server logs every request and its remote address. |

Public RPC and LCD serve everything CometBFT and the SDK serve, including calls
whose cost the caller picks (`tx_search` over the index, paginated queries,
simulate). A node open to the public should sit behind a request filter of its
operator's choosing.

## Ports

    1317   LCD
    26656  p2p    other nodes
    26657  RPC

gRPC (9090) is not published. p2p needs `EARTHD_P2P_EXTERNAL_ADDRESS` (or
`[p2p] external_address`) set to the address peers should dial: without it
CometBFT advertises the address it sees on itself, which in a container is a
private one. Seeds and persistent peers go in `[p2p] seeds` and
`persistent_peers` the same way.

## The volume is not optional

`/data` holds the node's config, keys and chain state. Without a volume every
restart is a new node with new keys and no history; the entrypoint decides
which case it is purely by whether `/data/config/genesis.json` is there.

## Genesis

`networks/genesis.json` is a build artifact, written by `scripts/build-genesis.sh`
from the sources in `networks/genesis/` and committed alongside its sha256. See
`networks/genesis/README.md`. Regenerate it with `make genesis` after any change
to its sources; `make genesis-check` fails if it has drifted. Until the launch
ceremony it is the placeholder set (`scripts/ceremony.sh`).

It carries the CSCA trust store, the passport register and privacy circuit
verifying keys, the ANML/ERTH pool, the liquidity auction, the retirement
schedules, the governance and consensus parameters (a 4 MiB block, 100M block
gas) and the genesis validator's gentx. The gentx names only the validator's
consensus *public* key; the private key never ships in the image.

## Validating, upgrades

A validator's consensus key belongs in a remote signer (`priv_validator_laddr`
in config.toml) or in a file the operator puts on the volume; this image neither
imports nor generates one beyond `earthd init`'s random key. Coordinated
upgrades halt the chain at the plan height; release tarballs are laid out for
cosmovisor (`.github/workflows/release.yml`), which an operator image can add.
