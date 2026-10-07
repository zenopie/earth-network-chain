# Container image — earth node

The image and the entrypoint here are what a node runs. Deployment (compose
files, the Akash SDL, secrets) lives in a separate private repository, which
resolves tag -> digest from the registry when it deploys.

Pushing a `v*.*.*` tag has CI build the image and push it to
`ghcr.io/zenopie/earth-network-chain`. Tags only — a plain push to `master`
builds nothing, and CI writes the digest nowhere.

    Dockerfile                          builds earthd on a slim runtime
    docker/entrypoint.sh                first-boot genesis, then earthd start
    .github/workflows/docker-build.yml  builds and pushes the image

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

## earth-edge: the filter in front of RPC and LCD

The image also carries `earth-edge` (`docker/edge/`), a small standard-library
Go program that a public node runs as a separate service in front of its RPC
and LCD:

    rpc.* -> cloudflared -> edge:26657 -> node:26657
    lcd.* -> cloudflared -> edge:1317  -> node:1317

It serves an allowlist: the CometBFT methods and `abci_query` paths the
wallets, the web app, the backend, `earthd --node` and state sync use
(`filter/rpcpolicy.go`), and the LCD routes those clients call
(`filter/lcdpolicy.go`). It decodes every request once, exactly as CometBFT
v0.38 and grpc-gateway v1.16 would, and forwards a request it wrote itself,
so an encoding the node would read differently never reaches the node. It
caps how many calls of each cost class (simulate, raw store reads, the tx
search, ...) run at once, keeps no per-client state, and logs no request.
Websockets, `tx_search`, `block_search`, batches and the LCD's
method-override and form POSTs are refused.

Run it as `earth`, not root (it refuses root):

    setpriv --reuid=earth --regid=earth --clear-groups --no-new-privs earth-edge

Configuration is `EDGE_RPC_UPSTREAM`, `EDGE_LCD_UPSTREAM` (default
`http://node:26657`, `http://node:1317`), `EDGE_RPC_LISTEN`,
`EDGE_LCD_LISTEN` (`:26657`, `:1317`) and `EDGE_MAX_CONNS`. The deploy
repo's `akash/README.md` ("Public RPC and LCD") is the operator's side.

Tests: `cd docker/edge && go test ./...` (allowlists, every bypass from the
audits, fuzzers); `cd docker/edge/conformance && go test ./...` checks the
filter against CometBFT's own argument decoding and every grpc-gateway route
in the SDK's and this repo's protos. Adding a route a client needs means a
line in `lcdpolicy.go` (or `abciGRPCExtra`) and a case in `filter_test.go`.

## The volume is not optional

`earth-data:/data` holds genesis, the validator's consensus key and all chain
state. Without it a redeploy does not restart the chain — it creates a *different*
one, with a new genesis and new keys, and every address the apps knew about stops
existing. The entrypoint decides which case it is purely by whether
`/data/config/genesis.json` is there.

## Genesis

`networks/genesis.json` is a build artifact, written by `scripts/build-genesis.sh`
from the sources in `networks/genesis/` and committed alongside its sha256. See
`networks/genesis/README.md`.

It carries the 536 CSCAs, the 33 passport register verifying keys, the four
privacy circuit keys (action, membership, stake, vote), the ANML/ERTH pool, the
liquidity auction, the retirement schedules, the governance parameters and the
genesis validator's gentx. The gentx names only the validator's consensus
*public* key; the private key never ships in the image.

## Three boot paths

The entrypoint picks one and says which. The difference between the first two is
the difference between joining a network and creating one.

| condition | what happens |
| --- | --- |
| `/data/config/genesis.json` exists | **resume** — start on the chain already in the volume |
| otherwise (default) | **join** — install `/etc/earth/genesis.json`, verify it against `/etc/earth/genesis.json.sha256`, start. No key created, no timestamp rewritten |
| `DEV_INIT=1` | **devnet** — generate a validator, stamp `genesis_time` to now, collect a gentx. A *new chain* every time |

The join path is the default: every container from the same image joins the
same chain. A hash mismatch is fatal — a genesis swapped into the image after the
fact fails loudly instead of quietly forking whoever runs it.

`DEV_INIT=1` is for a throwaway devnet and must never be set on a network node:
each node would stamp its own `genesis_time` and mint its own validator. It
rewrites the genesis, so a devnet's genesis can never be mistaken for the
release: the hash no longer matches.

## Browser access

The two surfaces behave differently and only one can be scoped.

| | port | setting | granularity |
| --- | --- | --- | --- |
| RPC | 26657 | `RPC_CORS_ORIGINS=https://a,https://b` | an allowlist |
| LCD | 1317 | `API_UNSAFE_CORS=1` | `*` or nothing — all the SDK offers |

**`RPC_CORS_ORIGINS` closes a gap that has always been open.** CometBFT ships
`cors_allowed_origins = []`, so a browser could never reach the RPC
cross-origin. CosmJS talks to the RPC, so anything the
page does itself was blocked. Keplr masks it: signing is in the extension and
`keplr.sendTx` broadcasts from its background context, neither subject to page
CORS.

It is re-applied on every start, so an origin can be added or removed with a
restart rather than a volume wipe.

Two flags are off unless asked for:

- **`API_UNSAFE_CORS=1`** — any origin may read the LCD *and broadcast through
  it*. Fine on a public read-only node, wrong on a block producer.
- **`--keyring-backend test`** only appears on the `DEV_INIT` path. The join
  path creates no keys at all, and a real validator's consensus key belongs
  behind `PRIV_VALIDATOR_LADDR`.

Run `docker/entrypoint_test.sh` to exercise all of it without building a
container.

Until the launch ceremony, `networks/genesis/accounts.json` is the placeholder
set, which includes two devnet accounts (the faucet and the ads-for-gas hot
wallet, 10,000 ERTH each); `scripts/ceremony.sh` removes them. See
`networks/genesis/README.md`.

On the `DEV_INIT` path the entrypoint stamps `genesis_time` to the current time
before the validator is created: CometBFT gives block 1 exactly the genesis time
while block 2 gets the wall clock, so the emission, prorated against elapsed
time, would otherwise pay the whole gap out in a single block. The join path
keeps the release's `genesis_time`.

Regenerate `networks/genesis.json` with `make genesis` after any change to its sources in
`networks/genesis/`; `make genesis-check` fails if it has drifted.

## After the first boot (DEV_INIT)

The devnet validator's key lives in the test keyring on the volume:

    earthd keys list --keyring-backend test --home /data

Point the backend at this node's LCD:

    EARTH_NODE_URL=rest+https://<host>:1317

## Defaults

`MIN_GAS_PRICES` defaults to `0.005uerth` and `API_UNSAFE_CORS` to off; both are
environment variables read on every start (`docker/entrypoint.sh`).
